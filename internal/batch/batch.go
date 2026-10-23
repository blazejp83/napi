// Package batch finds videos and downloads their subtitles in parallel.
package batch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blazejp83/napi/internal/config"
	"github.com/blazejp83/napi/internal/media"
	"github.com/blazejp83/napi/internal/napi"
	"github.com/blazejp83/napi/internal/osdb"
	"github.com/blazejp83/napi/internal/subs"
	"github.com/blazejp83/napi/internal/subsync"
)

type Status string

// Step is a stage of processing one video, reported through Runner.OnStep.
type Step string

const (
	StepHash     Step = "hash"     // reading the file
	StepNapi     Step = "napi"     // asking NapiProjekt
	StepOS       Step = "os"       // searching OpenSubtitles
	StepDownload Step = "download" // downloading a candidate
	StepSync     Step = "sync"     // aligning to the audio
)

func (r *Runner) step(video string, s Step) {
	if r.OnStep != nil {
		r.OnStep(video, s)
	}
}

// Source says where downloaded subtitles came from.
type Source string

const (
	FromNapi     Source = "napiprojekt"
	FromOS       Source = "opensubtitles"
	FromOSSynced Source = "opensubtitles+sync"
	// FromNapiOther is another version from a napiprojekt: link, synced.
	FromNapiOther Source = "napiprojekt+sync"
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
	Machine bool   // OpenSubtitles marks the subtitles as machine-translated
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
	Feature(ctx context.Context, imdbID int) (*osdb.Feature, error)
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
	// OnStep reports what is being done for a video; it may be called
	// concurrently from several workers.
	OnStep func(video string, step Step)

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
	if ex := r.existing(video, r.Config.Languages[0]); len(ex) > 0 && !r.Config.Overwrite {
		res.Status, res.Output, res.Lang = Skipped, ex[0], r.Config.Languages[0]
		return res
	}
	r.step(video, StepHash)
	hash, err := napi.FileHash(video)
	if err != nil {
		res.Status, res.Err = Failed, err
		return res
	}
	var osHits []osdb.Subtitle
	osSearched := false
	for i, lang := range r.Config.Languages {
		// A fallback language that is already there ends the search: PL was
		// not found and the ENG subtitles next to the video stay.
		if i > 0 && !r.Config.Overwrite {
			if ex := r.existing(video, lang); len(ex) > 0 {
				res.Status, res.Output, res.Lang = Skipped, ex[0], lang
				return res
			}
		}
		r.step(video, StepNapi)
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
			r.step(video, StepOS)
			osHits = r.osSearchHash(ctx, video, &res)
		}
		for _, s := range osdb.Rank(osHits, filepath.Base(video), 0, []string{osLang(lang)}) {
			if !s.MovieHashMatch {
				continue
			}
			r.step(video, StepDownload)
			content, err := r.osDownload(ctx, s, &res)
			if err != nil {
				break
			}
			res.Lang, res.Source = lang, FromOS
			res.Machine = s.AITranslated || s.MachineTranslated
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

// similar finds subtitles made for another release of the same movie (or
// episode) on OpenSubtitles and syncs the best candidate to the video's audio.
func (r *Runner) similar(ctx context.Context, video, hash string, hashHits []osdb.Subtitle, res *Result) (Result, bool) {
	r.step(video, StepOS)
	_, cands, ok := r.otherReleases(ctx, video, hash, hashHits, res)
	if !ok {
		return Result{}, false
	}
	fps, _, _ := media.FrameRate(video)
	ranked := osdb.Rank(cands, filepath.Base(video), fps, r.osLangs())
	// Each download counts against the daily limit; three candidates cover
	// a wrong length or a failed sync without burning through it.
	for _, s := range ranked[:min(3, len(ranked))] {
		r.step(video, StepDownload)
		content, err := r.osDownload(ctx, s, res)
		if err != nil {
			return Result{}, false
		}
		lang := strings.ToUpper(s.Language)
		if lang == "EN" {
			lang = "ENG"
		}
		if err := r.syncContent(ctx, video, content, lang, res); err != nil {
			addWarning(res, fmt.Sprintf("%s: %v", s.Release, err))
			if errors.Is(err, errNoSyncer) {
				return Result{}, false
			}
			continue
		}
		res.Source, res.Release = FromOSSynced, s.Release
		res.Machine = s.AITranslated || s.MachineTranslated
		return r.finish(*res, nil), true
	}
	return Result{}, false
}

// otherReleases searches OpenSubtitles for subtitles of the same movie or
// episode. A title search is first narrowed to one movie, then repeated by
// its IMDb ID for the complete list.
func (r *Runner) otherReleases(ctx context.Context, video, hash string, hashHits []osdb.Subtitle, res *Result) (osdb.Query, []osdb.Subtitle, bool) {
	q, ok := r.similarQuery(ctx, video, hash, hashHits, res)
	if !ok {
		return q, nil, false
	}
	cands, err := r.osSearch(ctx, q)
	if err != nil {
		r.osFailed(err, res)
		return q, nil, false
	}
	if q.IMDbID != 0 {
		return q, cands, true
	}
	if q.Episode > 0 {
		return q, osdb.SameTitle(cands, q.Text), true
	}
	imdb := r.resolveMovie(ctx, cands, q.Text, q.Year)
	if imdb == 0 {
		return q, nil, true
	}
	q = osdb.Query{IMDbID: imdb, Languages: q.Languages}
	cands, err = r.osSearch(ctx, q)
	if err != nil {
		r.osFailed(err, res)
		return q, nil, false
	}
	return q, cands, true
}

// resolveMovie picks the movie a title search was about. Results are fuzzy
// ("A Nos Amours" also returns "A Christmas Story"), and their title is the
// English one ("To Our Loves"), so a candidate must have the right year and
// a matching title, original title, alternative title or release name.
func (r *Runner) resolveMovie(ctx context.Context, cands []osdb.Subtitle, title string, year int) int {
	seen := map[int]bool{}
	var order []osdb.Subtitle
	for _, s := range cands {
		if s.IMDbID > 0 && !seen[s.IMDbID] && (year == 0 || s.Year == 0 || abs(s.Year-year) <= 1) {
			seen[s.IMDbID] = true
			order = append(order, s)
		}
	}
	want := osdb.NormTitle(title)
	for _, s := range order {
		rel := osdb.NormTitle(s.Release)
		if osdb.TitleMatches(title, s.Title) || rel == want || strings.HasPrefix(rel, want+" ") {
			return s.IMDbID
		}
	}
	for i, s := range order {
		if i == 5 {
			break // the catalogue lookups are one request each
		}
		var f *osdb.Feature
		err := r.retry(ctx, func() (err error) {
			f, err = r.OS.Feature(ctx, s.IMDbID)
			return err
		})
		if err == nil && osdb.TitleMatches(title, f.Names()...) {
			return s.IMDbID
		}
	}
	return 0
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// similarQuery identifies the movie or episode to search for: from
// NapiProjekt's movie info, the IMDb ID OpenSubtitles' hash search returned,
// or the file name.
func (r *Runner) similarQuery(ctx context.Context, video, hash string, hashHits []osdb.Subtitle, res *Result) (osdb.Query, bool) {
	q := osdb.Query{Languages: r.osLangs()}
	g := osdb.GuessTitle(video)
	if m := r.movie(ctx, hash, res); m != nil && m.IMDbID > 0 {
		q.IMDbID = m.IMDbID
		if m.Series {
			// NapiProjekt gives the series' IMDb ID for episodes.
			q.Season, q.Episode = m.Season, m.Episode
			if q.Episode == 0 {
				q.Season, q.Episode = g.Season, g.Episode
			}
			if q.Episode == 0 {
				return q, false
			}
		}
		return q, true
	}
	for _, s := range hashHits {
		if s.IMDbID > 0 {
			q.IMDbID = s.IMDbID // the movie's or the episode's own ID
			return q, true
		}
	}
	// A bare title is too weak a signal ("Trailer.mkv"): require a year or
	// an episode number.
	if g.Title == "" || (g.Year == 0 && g.Episode == 0) {
		return q, false
	}
	q.Text, q.Year, q.Season, q.Episode = g.Title, g.Year, g.Season, g.Episode
	return q, true
}

var errNoSyncer = errors.New("sync unavailable")

// syncContent converts downloaded subtitles to SRT, checks their length
// against the video, aligns them to its audio and writes the result.
func (r *Runner) syncContent(ctx context.Context, video string, content []byte, lang string, res *Result) error {
	d, cues, err := toCues(video, content, r.Config.DefaultFPS)
	if err != nil {
		return err
	}
	if err := plausibleLength(video, cues); err != nil {
		return err
	}
	syncer, err := r.getSyncer(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", errNoSyncer, err)
	}
	r.step(video, StepSync)
	synced, err := r.runSync(ctx, syncer, video, subs.WriteSRT(cues))
	if err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	// Re-render alass's output: it writes LF line endings.
	if out, err := subs.Parse(synced, subs.SubRip, 0); err == nil {
		synced = subs.WriteSRT(out)
	}
	res.Charset, res.Format, res.Lang = d.charset, d.format, lang
	res.Output = OutputPath(video, lang, ".srt", r.Config.Naming)
	return r.writeSubs(video, lang, res.Output, subs.Encode(synced, r.Config.Encoding))
}

// SyncSubtitles aligns subtitles obtained elsewhere (a napiprojekt: link,
// a file) to video and saves them next to it.
func (r *Runner) SyncSubtitles(ctx context.Context, video string, content []byte, lang string, src Source, release string) Result {
	res := Result{Video: video, Source: src, Release: release}
	return r.finish(res, r.syncContent(ctx, video, content, lang, &res))
}

// plausibleLength rejects subtitles that cannot belong to the video, such as
// another episode or a much longer cut: the last lines must start within the
// video (allowing for a 25 vs 23.976 fps speed-up) and past its middle.
func plausibleLength(video string, cues []subs.Cue) error {
	dur, err := media.Duration(video)
	if err != nil || dur <= 0 || len(cues) == 0 {
		return nil // unknown: let the sync decide
	}
	// Judge by start times, near the end but not the very last: a single
	// mistyped timestamp must not reject an otherwise good file.
	starts := make([]time.Duration, len(cues))
	for i, c := range cues {
		starts[i] = c.Start
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	last := starts[len(starts)-1-len(starts)/100]
	if last > dur*106/100+90*time.Second || last < dur/2 {
		return fmt.Errorf("length %s does not fit the video (%s)", clock(last), clock(dur))
	}
	return nil
}

func clock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
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

// toCues decodes downloaded subtitles and parses them into cues.
func toCues(video string, content []byte, defaultFPS float64) (decoded, []subs.Cue, error) {
	text, charset := subs.Decode(content)
	d := decoded{charset: charset, format: subs.Detect(text)}
	if d.format == subs.Unknown {
		return d, nil, errors.New("unknown subtitle format")
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
	return d, cues, err
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
	out := OutputPath(video, lang, ext, r.Config.Naming)
	res.Output = out
	return r.writeSubs(video, lang, out, subs.Encode(text, r.Config.Encoding))
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

// Language codes for file names and for recognising existing files.
var (
	langISO3 = map[string]string{"PL": "pol", "ENG": "eng"}
	langISO2 = map[string]string{"PL": "pl", "ENG": "en"}
	langTags = map[string][]string{"PL": {"pl", "pol", "polish", "polski"}, "ENG": {"en", "eng", "english"}}
)

// OutputPath is the subtitle file for video: Film.pol.srt with ISO 639-2
// naming, Film.pl.srt with ISO 639-1, Film.srt with none.
func OutputPath(video, lang, ext, naming string) string {
	base := strings.TrimSuffix(video, filepath.Ext(video))
	switch naming {
	case config.NamingNone:
	case config.NamingISO2:
		base += "." + langISO2[lang]
	default:
		base += "." + langISO3[lang]
	}
	return base + ext
}

func (r *Runner) existing(video, lang string) []string {
	return ExistingFor(video, lang, lang == r.Config.Languages[0])
}

// ExistingFor lists subtitle files next to video in language lang: tagged
// ones (Film.pl.srt, Film.pol.srt, Film.polish.srt) and untagged ones
// (Film.srt) whose text is in that language. Untagged files whose language
// cannot be recognised count for the primary language only.
func ExistingFor(video, lang string, primary bool) []string {
	dir := filepath.Dir(video)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(video), filepath.Ext(video)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		ext := filepath.Ext(name)
		if e.IsDir() || !subtitleExts[ext] || !strings.HasPrefix(name, base+".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		tags := strings.FieldsFunc(strings.TrimSuffix(name[len(base):], ext), func(r rune) bool {
			return r == '.' || r == '-' || r == '_' || r == ' '
		})
		switch tagLang(tags) {
		case lang:
			out = append(out, path)
		case "":
			// No language in the name (Film.srt, Film.forced.srt): read it.
			if l := fileLanguage(path); l == lang || (l == "" && primary) {
				out = append(out, path)
			}
		}
	}
	return out
}

func tagLang(tags []string) string {
	for _, t := range tags {
		for lang, codes := range langTags {
			for _, c := range codes {
				if t == c {
					return lang
				}
			}
		}
	}
	return ""
}

func fileLanguage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	n, _ := io.ReadFull(f, buf)
	text, _ := subs.Decode(buf[:n])
	return subs.DetectLanguage(text)
}

// writeSubs writes new subtitles for video, first moving any existing ones
// in the same language (and the target file) aside as <name>.bak.
func (r *Runner) writeSubs(video, lang, out string, data []byte) error {
	old := r.existing(video, lang)
	if _, err := os.Stat(out); err == nil {
		old = append(old, out)
	}
	seen := map[string]bool{}
	for _, p := range old {
		if seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		os.Remove(p + ".bak")
		if err := os.Rename(p, p+".bak"); err != nil {
			return fmt.Errorf("backing up %s: %w", filepath.Base(p), err)
		}
	}
	return writeAtomic(out, data)
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
			if r.Source == FromOSSynced || r.Source == FromNapiOther || r.Source == FromResync {
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

// Candidates lists what the OpenSubtitles fallback would try for video, best
// first, without downloading: exact hash matches, then other releases.
func (r *Runner) Candidates(ctx context.Context, video string) (osdb.Query, *napi.Movie, []osdb.Subtitle, error) {
	if r.OS == nil {
		return osdb.Query{}, nil, nil, errors.New("OpenSubtitles is not configured")
	}
	hash, err := napi.FileHash(video)
	if err != nil {
		return osdb.Query{}, nil, nil, err
	}
	var res Result
	hits := r.osSearchHash(ctx, video, &res)
	var cands []osdb.Subtitle
	for _, s := range hits {
		if s.MovieHashMatch {
			cands = append(cands, s)
		}
	}
	q, more, _ := r.otherReleases(ctx, video, hash, hits, &res)
	cands = append(cands, more...)
	fps, _, _ := media.FrameRate(video)
	ranked := osdb.Rank(dedupeSubs(cands), filepath.Base(video), fps, r.osLangs())
	if res.Warning != "" && len(ranked) == 0 {
		err = errors.New(res.Warning)
	}
	return q, res.movie, ranked, err
}

func dedupeSubs(in []osdb.Subtitle) []osdb.Subtitle {
	seen := map[string]bool{}
	var out []osdb.Subtitle
	for _, s := range in {
		if !seen[s.ID] {
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	return out
}

// FromResync marks subtitles that were already next to the video and were
// re-aligned to its audio ("Synchronizuj napisy").
const FromResync Source = "resync"

// FindVideo finds the video a subtitle file belongs to, in the same folder:
// Film.pol.srt, Film.pl.forced.srt and Film.srt all belong to Film.mkv.
func FindVideo(sub string, exts []string) (string, error) {
	dir := filepath.Dir(sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	isVideo := map[string]bool{}
	for _, e := range exts {
		isVideo["."+strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	videos := map[string]string{} // lower-case base name -> file name
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && isVideo[ext] {
			videos[strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))] = e.Name()
		}
	}
	base := strings.TrimSuffix(filepath.Base(sub), filepath.Ext(sub))
	for b := base; b != ""; {
		if v, ok := videos[strings.ToLower(b)]; ok {
			return filepath.Join(dir, v), nil
		}
		i := strings.LastIndex(b, ".")
		if i <= 0 {
			break
		}
		b = b[:i]
	}
	return "", fmt.Errorf("no video file for %s in its folder", filepath.Base(sub))
}

// SyncFiles re-aligns existing subtitle files to their videos' audio, one
// at a time. Each result is saved under the subtitle's name with .srt; the
// file it replaces is kept as <name>.bak.
func (r *Runner) SyncFiles(ctx context.Context, files []string) []Result {
	results := make([]Result, 0, len(files))
	for i, sub := range files {
		res := r.syncFile(ctx, sub)
		results = append(results, res)
		if r.Progress != nil {
			r.Progress(i+1, len(files), res)
		}
		if ctx.Err() != nil {
			for _, rest := range files[i+1:] {
				results = append(results, Result{Video: rest, Status: Failed, Err: ctx.Err()})
			}
			break
		}
	}
	return results
}

func (r *Runner) syncFile(ctx context.Context, sub string) Result {
	res := Result{Video: sub, Source: FromResync}
	video, err := FindVideo(sub, r.Config.Extensions)
	if err != nil {
		return r.finish(res, err)
	}
	res.Video = video
	content, err := os.ReadFile(sub)
	if err != nil {
		return r.finish(res, err)
	}
	d, cues, err := toCues(video, content, r.Config.DefaultFPS)
	if err != nil {
		return r.finish(res, err)
	}
	ext := filepath.Ext(sub)
	tags := strings.FieldsFunc(strings.ToLower(strings.TrimSuffix(filepath.Base(sub), ext)), func(r rune) bool { return r == '.' })
	res.Lang = tagLang(tags)
	if res.Lang == "" {
		text, _ := subs.Decode(content)
		res.Lang = subs.DetectLanguage(text)
	}
	syncer, err := r.getSyncer(ctx)
	if err != nil {
		return r.finish(res, fmt.Errorf("%w: %v", errNoSyncer, err))
	}
	r.step(video, StepSync)
	synced, err := r.runSync(ctx, syncer, video, subs.WriteSRT(cues))
	if err != nil {
		return r.finish(res, fmt.Errorf("sync failed: %w", err))
	}
	if out, err := subs.Parse(synced, subs.SubRip, 0); err == nil {
		synced = subs.WriteSRT(out)
	}
	res.Charset, res.Format, res.Release = d.charset, d.format, filepath.Base(sub)
	res.Output = strings.TrimSuffix(sub, ext) + ".srt"
	if _, err := os.Stat(res.Output); err == nil {
		os.Remove(res.Output + ".bak")
		if err := os.Rename(res.Output, res.Output+".bak"); err != nil {
			return r.finish(res, fmt.Errorf("backing up %s: %w", filepath.Base(res.Output), err))
		}
	}
	return r.finish(res, writeAtomic(res.Output, subs.Encode(synced, r.Config.Encoding)))
}

// IsSubtitleFile reports whether path has a subtitle extension napi can sync.
func IsSubtitleFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".srt", ".sub", ".txt", ".mpl":
		return true
	}
	return false
}
