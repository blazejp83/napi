// Package batch finds videos and downloads their subtitles in parallel.
package batch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/media"
	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/osdb"
	"github.com/blaze/napi/internal/subs"
	"github.com/blaze/napi/internal/subsync"
)

type Status string

// Source says where downloaded subtitles came from.
type Source string

const (
	FromNapi     Source = "napiprojekt"
	FromOS       Source = "opensubtitles"
	FromOSSynced Source = "opensubtitles+sync"
)

const (
	Downloaded Status = "ok"
	NotFound   Status = "not-found"
	Skipped    Status = "exists"
	Failed     Status = "error"
)

// Result describes what happened to one video.
type Result struct {
	Video   string
	Status  Status
	Lang    string
	Format  string // original subtitle format
	Charset string // original charset
	FPS     float64
	FPSFrom string // mkv, mp4, avi, ffprobe, subtitle, default
	Source  Source
	Release string // release the subtitles were made for, when synced
	// Movie is set for videos left without subtitles that NapiProjekt
	// recognises: it likely has subtitles for other versions on its website.
	Movie *napi.Movie

	movie    *napi.Movie // cached MovieInfo result while processing
	lookedUp bool
	Output   string
	Warning  string
	Err      error
}

var subtitleExts = map[string]bool{".srt": true, ".txt": true, ".sub": true, ".ass": true,
	".ssa": true, ".vtt": true, ".smi": true, ".mpl": true}

// skipDirs are never scanned: system folders and the extras folders used by
// Plex/Kodi naming, whose clips would otherwise be looked up as movies.
var skipDirs = map[string]bool{"$recycle.bin": true, "system volume information": true,
	"featurettes": true, "extras": true, "extra": true, "trailers": true, "behind the scenes": true,
	"deleted scenes": true, "interviews": true, "scenes": true, "shorts": true, "samples": true, "sample": true,
	"bonus": true}

// Collect expands paths into a sorted, de-duplicated list of video files.
// Files are taken as given; folders are scanned for known video extensions,
// skipping "sample" clips.
func Collect(paths []string, cfg config.Config) ([]string, error) {
	exts := map[string]bool{}
	for _, e := range cfg.Extensions {
		exts["."+strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(root)
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are skipped, not fatal
			}
			if d.IsDir() {
				if p != root && (!cfg.Recursive || skipDirs[strings.ToLower(d.Name())]) {
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.ToLower(d.Name())
			if exts[filepath.Ext(name)] && !strings.Contains(name, "sample") {
				add(p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// Downloader fetches subtitles; *napi.Client implements it.
type Downloader interface {
	Download(ctx context.Context, hash, lang string) (*napi.Subtitles, error)
}

// MovieLookup finds the movie a NapiProjekt hash belongs to; *napi.Client implements it.
type MovieLookup interface {
	MovieInfo(ctx context.Context, hash string) (*napi.Movie, error)
}

// OpenSubtitles searches and downloads; *osdb.Client implements it.
type OpenSubtitles interface {
	Search(ctx context.Context, q osdb.Query) ([]osdb.Subtitle, error)
	Download(ctx context.Context, fileID int) ([]byte, error)
}

// Syncer aligns SubRip text to a video's audio; *subsync.Tools implements it.
type Syncer interface {
	Sync(ctx context.Context, video, srt string) (string, error)
}

// Runner processes videos with a bounded number of workers and a minimum
// gap between API requests.
type Runner struct {
	Config     config.Config
	Client     Downloader
	Movies     MovieLookup
	OS         OpenSubtitles // nil when no API key is configured
	RequestGap time.Duration
	// NewSyncer prepares the sync tool on first use (it may download it).
	NewSyncer func(ctx context.Context) (Syncer, error)
	// Progress is called after each video, from worker goroutines but never
	// concurrently.
	Progress func(done, total int, r Result)

	mu      sync.Mutex
	lastReq time.Time

	osMu  sync.Mutex
	osOff string // why OpenSubtitles was disabled for the rest of the run

	syncOnce sync.Once
	syncer   Syncer
	syncErr  error
	syncSlot chan struct{} // one sync at a time: each reads a whole audio track
}

func NewRunner(cfg config.Config) *Runner {
	nc := napi.NewClient(cfg.Client)
	r := &Runner{Config: cfg, Client: nc, Movies: nc, RequestGap: 200 * time.Millisecond}
	if o := cfg.OpenSubtitles; o.APIKey != "" {
		r.OS = osdb.NewClient(o.APIKey, o.Username, o.Password, o.UserAgent)
	}
	r.NewSyncer = func(ctx context.Context) (Syncer, error) { return subsync.Ensure(ctx, cfg.AlassPath) }
	return r
}

// Run processes all videos and returns their results in input order.
func (r *Runner) Run(ctx context.Context, videos []string) []Result {
	results := make([]Result, len(videos))
	idx := make(chan int)
	var wg sync.WaitGroup
	var progMu sync.Mutex
	done := 0
	for w := 0; w < r.Config.Jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				res := r.process(ctx, videos[i])
				results[i] = res
				progMu.Lock()
				done++
				if r.Progress != nil {
					r.Progress(done, len(videos), res)
				}
				progMu.Unlock()
			}
		}()
	}
dispatch:
	for i := range videos {
		select {
		case idx <- i:
		case <-ctx.Done():
			for j := i; j < len(videos); j++ {
				results[j] = Result{Video: videos[j], Status: Failed, Err: ctx.Err()}
			}
			break dispatch
		}
	}
	close(idx)
	wg.Wait()
	return results
}

func (r *Runner) process(ctx context.Context, video string) Result {
	res := Result{Video: video}
	if !r.Config.Overwrite {
		if existing := ExistingSubtitles(video); existing != "" {
			res.Status, res.Output = Skipped, existing
			return res
		}
	}
	hash, err := napi.FileHash(video)
	if err != nil {
		res.Status, res.Err = Failed, err
		return res
	}
	var osHits []osdb.Subtitle
	osSearched := false
	for _, lang := range r.Config.Languages {
		sub, err := r.download(ctx, hash, lang)
		if err == nil {
			res.Lang, res.Source = sub.Lang, FromNapi
			return r.finish(res, r.save(video, sub.Content, sub.Lang, &res))
		}
		if !errors.Is(err, napi.ErrNotFound) {
			res.Status, res.Err = Failed, err
			return res
		}
		if r.osEnabled(&res) == nil {
			continue
		}
		if !osSearched {
			osSearched = true
			osHits = r.osSearchHash(ctx, video, &res)
		}
		for _, s := range osdb.Rank(osHits, filepath.Base(video), 0, []string{osLang(lang)}) {
			if !s.MovieHashMatch {
				continue
			}
			content, err := r.osDownload(ctx, s, &res)
			if err != nil {
				break
			}
			res.Lang, res.Source = lang, FromOS
			return r.finish(res, r.save(video, content, lang, &res))
		}
	}
	if r.Config.Sync && r.osEnabled(&res) != nil {
		if done, ok := r.similar(ctx, video, hash, osHits, &res); ok {
			return done
		}
	}
	if res.Status == "" {
		res.Status = NotFound
		// Every automatic source failed; if NapiProjekt knows the movie, its
		// website probably lists subtitles for other versions to pick by hand.
		res.Movie = r.movie(ctx, hash, &res)
	}
	return res
}

func (r *Runner) finish(res Result, err error) Result {
	if err != nil {
		res.Status, res.Err = Failed, err
		return res
	}
	res.Status = Downloaded
	return res
}

func osLang(lang string) string {
	if lang == "ENG" {
		return "en"
	}
	return strings.ToLower(lang)
}

func (r *Runner) osLangs() []string {
	var l []string
	for _, lang := range r.Config.Languages {
		l = append(l, osLang(lang))
	}
	return l
}

// osEnabled returns the OpenSubtitles client, or nil (noting why in res)
// when none is configured or an earlier error disabled it for this run.
func (r *Runner) osEnabled(res *Result) OpenSubtitles {
	if r.OS == nil {
		return nil
	}
	r.osMu.Lock()
	defer r.osMu.Unlock()
	if r.osOff != "" {
		addWarning(res, r.osOff)
		return nil
	}
	return r.OS
}

// osFailed records an OpenSubtitles error. Quota and login errors disable
// it for the rest of the run instead of failing every remaining video.
func (r *Runner) osFailed(err error, res *Result) {
	if errors.Is(err, osdb.ErrQuota) || errors.Is(err, osdb.ErrAuth) {
		r.osMu.Lock()
		if r.osOff == "" {
			r.osOff = err.Error()
		}
		r.osMu.Unlock()
	}
	addWarning(res, err.Error())
}

func addWarning(res *Result, w string) {
	if w == "" || strings.Contains(res.Warning, w) {
		return
	}
	if res.Warning != "" {
		res.Warning += "; "
	}
	res.Warning += w
}

func (r *Runner) osSearchHash(ctx context.Context, video string, res *Result) []osdb.Subtitle {
	h, _, err := osdb.FileHash(video)
	if err != nil {
		return nil
	}
	hits, err := r.osSearch(ctx, osdb.Query{MovieHash: h, Languages: r.osLangs()})
	if err != nil {
		r.osFailed(err, res)
	}
	return hits
}

func (r *Runner) osSearch(ctx context.Context, q osdb.Query) ([]osdb.Subtitle, error) {
	var hits []osdb.Subtitle
	err := r.retry(ctx, func() (err error) {
		hits, err = r.OS.Search(ctx, q)
		return err
	})
	return hits, err
}

func (r *Runner) osDownload(ctx context.Context, s osdb.Subtitle, res *Result) ([]byte, error) {
	var data []byte
	err := r.retry(ctx, func() (err error) {
		data, err = r.OS.Download(ctx, s.Files[0].ID)
		return err
	})
	if err != nil {
		r.osFailed(err, res)
	}
	return data, err
}

// similar finds subtitles made for another release of the same movie on
// OpenSubtitles and syncs the best candidate to the video's audio.
func (r *Runner) similar(ctx context.Context, video, hash string, hashHits []osdb.Subtitle, res *Result) (Result, bool) {
	q := osdb.Query{Languages: r.osLangs()}
	for _, s := range hashHits {
		if s.IMDbID > 0 {
			q.IMDbID = s.IMDbID
			break
		}
	}
	if q.IMDbID == 0 {
		if m := r.movie(ctx, hash, res); m != nil && m.IMDbID > 0 {
			q.IMDbID = m.IMDbID
		}
	}
	if q.IMDbID == 0 {
		// A bare title is too weak a signal ("Trailer.mkv"); require a year.
		q.Text, q.Year = osdb.GuessTitle(video)
		if q.Text == "" || q.Year == 0 {
			return Result{}, false
		}
	}
	cands, err := r.osSearch(ctx, q)
	if err != nil {
		r.osFailed(err, res)
		return Result{}, false
	}
	if q.IMDbID == 0 {
		cands = osdb.SameTitle(cands, q.Text)
	}
	fps, _, _ := media.FrameRate(video)
	ranked := osdb.Rank(cands, filepath.Base(video), fps, r.osLangs())
	if len(ranked) == 0 {
		return Result{}, false
	}
	syncer, err := r.getSyncer(ctx)
	if err != nil {
		addWarning(res, "sync unavailable: "+err.Error())
		return Result{}, false
	}
	// Try the two best candidates: a bad sync of the first usually means it
	// was made for a very different cut.
	for _, s := range ranked[:min(2, len(ranked))] {
		content, err := r.osDownload(ctx, s, res)
		if err != nil {
			return Result{}, false
		}
		text, srt, err := toSRT(video, content, r.Config.DefaultFPS)
		if err != nil {
			addWarning(res, fmt.Sprintf("%s: %v", s.Release, err))
			continue
		}
		synced, err := r.runSync(ctx, syncer, video, srt)
		if err != nil {
			addWarning(res, fmt.Sprintf("sync of %s failed: %v", s.Release, err))
			continue
		}
		res.Charset, res.Format = text.charset, text.format
		res.Lang = strings.ToUpper(s.Language)
		if res.Lang == "EN" {
			res.Lang = "ENG"
		}
		res.Source, res.Release = FromOSSynced, s.Release
		out := OutputPath(video, res.Lang, ".srt", r.Config.LangSuffix)
		res.Output = out
		return r.finish(*res, writeAtomic(out, subs.Encode(synced, r.Config.Encoding))), true
	}
	return Result{}, false
}

// movie looks up, once per video, the movie NapiProjekt associates with hash.
func (r *Runner) movie(ctx context.Context, hash string, res *Result) *napi.Movie {
	if res.lookedUp || r.Movies == nil {
		return res.movie
	}
	res.lookedUp = true
	r.retry(ctx, func() (err error) {
		res.movie, err = r.Movies.MovieInfo(ctx, hash)
		return err
	})
	return res.movie
}

func (r *Runner) getSyncer(ctx context.Context) (Syncer, error) {
	r.syncOnce.Do(func() {
		if r.NewSyncer == nil {
			r.syncErr = errors.New("no sync tool")
			return
		}
		r.syncer, r.syncErr = r.NewSyncer(ctx)
	})
	return r.syncer, r.syncErr
}

func (r *Runner) runSync(ctx context.Context, s Syncer, video, srt string) (string, error) {
	r.mu.Lock()
	if r.syncSlot == nil {
		r.syncSlot = make(chan struct{}, 1)
	}
	slot := r.syncSlot
	r.mu.Unlock()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-slot }()
	return s.Sync(ctx, video, srt)
}

type decoded struct{ charset, format string }

// toSRT decodes downloaded subtitles and converts them to SubRip text.
func toSRT(video string, content []byte, defaultFPS float64) (decoded, string, error) {
	text, charset := subs.Decode(content)
	d := decoded{charset: charset, format: subs.Detect(text)}
	if d.format == subs.Unknown {
		return d, "", errors.New("unknown subtitle format")
	}
	fps := 0.0
	if d.format == subs.MicroDVD {
		if fps = subs.DeclaredFPS(text); fps == 0 {
			if v, _, err := media.FrameRate(video); err == nil {
				fps = v
			} else {
				fps = defaultFPS
			}
		}
	}
	cues, err := subs.Parse(text, d.format, fps)
	if err != nil {
		return d, "", err
	}
	return d, subs.WriteSRT(cues), nil
}

// download calls the NapiProjekt API, spacing requests and retrying
// temporary failures.
func (r *Runner) download(ctx context.Context, hash, lang string) (*napi.Subtitles, error) {
	var sub *napi.Subtitles
	err := r.retry(ctx, func() (err error) {
		sub, err = r.Client.Download(ctx, hash, lang)
		return err
	})
	return sub, err
}

// retry runs call after the request gap, up to three times while it fails
// with a temporary error.
func (r *Runner) retry(ctx context.Context, call func() error) error {
	var err error
	for attempt, backoff := 0, time.Second; attempt < 3; attempt, backoff = attempt+1, backoff*2 {
		if err := r.wait(ctx); err != nil {
			return err
		}
		err = call()
		var tmp *napi.TemporaryError
		if !errors.As(err, &tmp) {
			return err
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (r *Runner) wait(ctx context.Context) error {
	r.mu.Lock()
	next := r.lastReq.Add(r.RequestGap)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	r.lastReq = next
	r.mu.Unlock()
	select {
	case <-time.After(time.Until(next)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) save(video string, content []byte, lang string, res *Result) error {
	text, charset := subs.Decode(content)
	res.Charset = charset
	res.Format = subs.Detect(text)
	ext := ".txt"
	if res.Format == subs.SubRip {
		ext = ".srt"
	}
	if r.Config.Format == "srt" && res.Format != subs.Unknown {
		fps := 0.0
		if res.Format == subs.MicroDVD {
			fps = r.frameRate(video, text, res)
		}
		if cues, err := subs.Parse(text, res.Format, fps); err == nil {
			text, ext = subs.WriteSRT(cues), ".srt"
		} else {
			res.Warning = fmt.Sprintf("kept original format: %v", err)
		}
	}
	out := OutputPath(video, lang, ext, r.Config.LangSuffix)
	res.Output = out
	return writeAtomic(out, subs.Encode(text, r.Config.Encoding))
}

func (r *Runner) frameRate(video, text string, res *Result) float64 {
	if fps := subs.DeclaredFPS(text); fps > 0 {
		res.FPS, res.FPSFrom = fps, "subtitle"
		return fps
	}
	if fps, src, err := media.FrameRate(video); err == nil {
		res.FPS, res.FPSFrom = fps, src
		return fps
	}
	res.FPS, res.FPSFrom = r.Config.DefaultFPS, "default"
	res.Warning = fmt.Sprintf("frame rate unknown, assumed %.3f fps", r.Config.DefaultFPS)
	return r.Config.DefaultFPS
}

// OutputPath is video's name with the subtitle extension, optionally with a
// language suffix (movie.pl.srt).
func OutputPath(video, lang, ext string, langSuffix bool) string {
	base := strings.TrimSuffix(video, filepath.Ext(video))
	if langSuffix {
		code := "pl"
		if lang == "ENG" {
			code = "en"
		}
		base += "." + code
	}
	return base + ext
}

// ExistingSubtitles returns a subtitle file that already belongs to video
// (movie.srt, movie.txt, movie.pl.srt, ...), or "".
func ExistingSubtitles(video string) string {
	dir := filepath.Dir(video)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(video), filepath.Ext(video)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		ext := filepath.Ext(name)
		if e.IsDir() || !subtitleExts[ext] || !strings.HasPrefix(name, base+".") {
			continue
		}
		// Allow a short tag between the name and extension: .pl, .eng, .forced
		if middle := strings.TrimSuffix(name[len(base):], ext); len(middle) <= 7 {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".napi-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.Chmod(0o644)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Summary counts results by status.
type Summary struct {
	Downloaded, NotFound, Skipped, Failed int
	Synced                                int // of Downloaded, aligned from another release
}

func Summarize(results []Result) Summary {
	var s Summary
	for _, r := range results {
		switch r.Status {
		case Downloaded:
			s.Downloaded++
			if r.Source == FromOSSynced {
				s.Synced++
			}
		case NotFound:
			s.NotFound++
		case Skipped:
			s.Skipped++
		case Failed:
			s.Failed++
		}
	}
	return s
}

// ExitCode is 0 when every video has subtitles, 2 when some were not found
// and 1 on any error.
func (s Summary) ExitCode() int {
	switch {
	case s.Failed > 0:
		return 1
	case s.NotFound > 0:
		return 2
	}
	return 0
}
