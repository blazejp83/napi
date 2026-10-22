package batch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/osdb"
)

type fakeOS struct {
	mu        sync.Mutex
	byHash    map[string][]osdb.Subtitle
	byIMDb    map[int][]osdb.Subtitle
	files     map[int]string
	quotaFrom int // downloads after this many fail with ErrQuota (0 = never)
	features  map[int]*osdb.Feature
	downloads int
	queries   []osdb.Query
}

func (f *fakeOS) Search(_ context.Context, q osdb.Query) ([]osdb.Subtitle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if q.MovieHash != "" {
		return f.byHash[q.MovieHash], nil
	}
	return f.byIMDb[q.IMDbID], nil
}

func (f *fakeOS) Feature(_ context.Context, id int) (*osdb.Feature, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ft, ok := f.features[id]; ok {
		return ft, nil
	}
	return nil, errors.New("no such feature")
}

func (f *fakeOS) Download(_ context.Context, id int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	if f.quotaFrom > 0 && f.downloads > f.quotaFrom {
		return nil, osdb.ErrQuota
	}
	return []byte(f.files[id]), nil
}

type fakeMovies map[string]int // napi hash -> IMDb ID

func (m fakeMovies) MovieInfo(_ context.Context, hash string) (*napi.Movie, error) {
	if id, ok := m[hash]; ok {
		return &napi.Movie{ID: "m", IMDbID: id}, nil
	}
	return nil, napi.ErrNotFound
}

// shiftSyncer pretends to align by replacing the first timestamp.
type shiftSyncer struct{ calls []string }

func (s *shiftSyncer) Sync(_ context.Context, video, srt string) (string, error) {
	s.calls = append(s.calls, filepath.Base(video))
	return strings.Replace(srt, "00:00:01,000", "00:00:03,500", 1), nil
}

func osHash(t *testing.T, p string) string {
	h, _, err := osdb.FileHash(p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestFallbackToOpenSubtitles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("v", 70*1024)
	exact := write(t, filepath.Join(dir, "Exact.mkv"), big+"exact")
	similar := write(t, filepath.Join(dir, "Movie.2007.1080p.BluRay.x265-GRP.mkv"), big+"similar")
	limited := write(t, filepath.Join(dir, "Limited.mkv"), big+"limited")
	napiOnly := write(t, filepath.Join(dir, "Napi.mkv"), big+"napi")

	srt := "1\r\n00:00:01,000 --> 00:00:02,000\r\nCześć\r\n\r\n"
	one := func(id int) []osdb.File { return []osdb.File{{ID: id}} }
	fos := &fakeOS{
		byHash: map[string][]osdb.Subtitle{
			osHash(t, exact): {{ID: "e", Language: "pl", MovieHashMatch: true, Files: one(1)}},
			// Hash search finds the movie (IMDb) but no subtitles for this file.
			osHash(t, similar): {{ID: "x", Language: "pl", IMDbID: 0, Files: one(9)}},
		},
		byIMDb: map[int][]osdb.Subtitle{
			1032846: {
				{ID: "dvd", Language: "pl", Release: "Movie.DVDRip.XviD", Files: one(2)},
				{ID: "bd", Language: "pl", Release: "Movie.2007.1080p.BluRay.x264", Files: one(3)},
			},
			555: {{ID: "l", Language: "pl", Release: "Limited", Files: one(4)}},
		},
		files:     map[int]string{1: srt, 2: srt, 3: srt, 4: srt},
		quotaFrom: 2,
	}
	syncer := &shiftSyncer{}
	cfg := config.Default()
	cfg.Encoding = "utf8"
	cfg.Jobs = 1 // deterministic download order for the quota check
	r := &Runner{
		Config:    cfg,
		Client:    &fakeAPI{subs: map[string]string{hashOf(t, napiOnly) + "/PL": srt}},
		Movies:    fakeMovies{hashOf(t, similar): 1032846, hashOf(t, limited): 555},
		OS:        fos,
		NewSyncer: func(context.Context) (Syncer, error) { return syncer, nil },
	}
	results := r.Run(context.Background(), []string{exact, similar, limited, napiOnly})
	by := map[string]Result{}
	for _, res := range results {
		by[filepath.Base(res.Video)] = res
	}

	if e := by["Exact.mkv"]; e.Status != Downloaded || e.Source != FromOS {
		t.Errorf("Exact: %+v", e)
	}
	s := by["Movie.2007.1080p.BluRay.x265-GRP.mkv"]
	if s.Status != Downloaded || s.Source != FromOSSynced || s.Release != "Movie.2007.1080p.BluRay.x264" {
		t.Errorf("Similar: %+v", s)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "Movie.2007.1080p.BluRay.x265-GRP.pol.srt"))
	if !strings.Contains(string(out), "00:00:03,500") {
		t.Errorf("synced output not written: %q", out)
	}
	if l := by["Limited.mkv"]; l.Status != NotFound || !strings.Contains(l.Warning, "limit") {
		t.Errorf("Limited: %+v", l)
	}
	if n := by["Napi.mkv"]; n.Status != Downloaded || n.Source != FromNapi {
		t.Errorf("Napi: %+v", n)
	}
	if len(syncer.calls) != 1 {
		t.Errorf("sync calls %v, want only the similar video", syncer.calls)
	}
	// After the quota error OpenSubtitles is off: Napi.mkv never searched it.
	for _, q := range fos.queries {
		if q.MovieHash == osHash(t, napiOnly) {
			t.Error("OpenSubtitles queried for a video NapiProjekt already had")
		}
	}
	if sum := Summarize(results); sum.Synced != 1 || sum.Downloaded != 3 {
		t.Errorf("summary %+v", sum)
	}
}

func TestSyncDisabled(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Movie.mkv"), strings.Repeat("v", 70*1024))
	fos := &fakeOS{byIMDb: map[int][]osdb.Subtitle{7: {{ID: "a", Language: "pl", Files: []osdb.File{{ID: 1}}}}},
		files: map[int]string{1: "1\r\n00:00:01,000 --> 00:00:02,000\r\nx\r\n"}}
	cfg := config.Default()
	cfg.Sync = false
	r := &Runner{Config: cfg, Client: &fakeAPI{}, Movies: fakeMovies{hashOf(t, v): 7}, OS: fos}
	if res := r.Run(context.Background(), []string{v})[0]; res.Status != NotFound || fos.downloads != 0 {
		t.Fatalf("%+v, downloads %d", res, fos.downloads)
	}
}

func TestExtrasAndTitleGuard(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("v", 70*1024)
	movie := write(t, filepath.Join(dir, "Film (2007)", "Film (2007).mkv"), big+"m")
	write(t, filepath.Join(dir, "Film (2007)", "Featurettes", "Trailer.mkv"), big+"t")
	noYear := write(t, filepath.Join(dir, "Other", "Deleted Scenes.mkv"), big+"d")
	videos, err := Collect([]string{dir}, config.Default())
	if err != nil || len(videos) != 2 || videos[0] != movie || videos[1] != noYear {
		t.Fatalf("collected %v %v", videos, err)
	}
	one := []osdb.File{{ID: 1}}
	fos := &fakeOS{files: map[int]string{1: "1\r\n00:00:01,000 --> 00:00:02,000\r\nx\r\n"},
		byIMDb: map[int][]osdb.Subtitle{
			0: {
				{ID: "other", Language: "pl", Title: "Film Noir", IMDbID: 11, Files: one},
				{ID: "right", Language: "pl", Title: "Film", IMDbID: 12, Release: "Film.2007", Files: one},
			},
			12: {{ID: "right", Language: "pl", Title: "Film", IMDbID: 12, Release: "Film.2007", Files: one}},
		}}
	syncer := &shiftSyncer{}
	r := &Runner{Config: config.Default(), Client: &fakeAPI{}, OS: fos,
		NewSyncer: func(context.Context) (Syncer, error) { return syncer, nil }}
	results := r.Run(context.Background(), videos)
	if results[0].Status != Downloaded || results[0].Release != "Film.2007" {
		t.Errorf("movie: %+v", results[0])
	}
	if results[1].Status != NotFound {
		t.Errorf("no-year clip: %+v", results[1])
	}
	for _, q := range fos.queries {
		if q.Text == "Deleted Scenes" {
			t.Error("searched OpenSubtitles by title without a year")
		}
	}
}

func TestRecognisedMovieHint(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("v", 70*1024)
	known := write(t, filepath.Join(dir, "Known.mkv"), big+"k")
	unknown := write(t, filepath.Join(dir, "Unknown.mkv"), big+"u")
	exactOS := write(t, filepath.Join(dir, "ExactOS.mkv"), big+"e")
	movies := &countingMovies{m: fakeMovies{hashOf(t, known): 1, hashOf(t, exactOS): 2}}
	fos := &fakeOS{
		byHash: map[string][]osdb.Subtitle{osHash(t, exactOS): {{ID: "e", Language: "pl", MovieHashMatch: true, Files: []osdb.File{{ID: 1}}}}},
		files:  map[int]string{1: "1\r\n00:00:01,000 --> 00:00:02,000\r\nx\r\n"},
	}
	r := &Runner{Config: config.Default(), Client: &fakeAPI{}, Movies: movies, OS: fos,
		NewSyncer: func(context.Context) (Syncer, error) { return &shiftSyncer{}, nil }}
	res := r.Run(context.Background(), []string{known, unknown, exactOS})
	if res[0].Status != NotFound || res[0].Movie == nil || res[0].Movie.IMDbID != 1 {
		t.Errorf("known: %+v", res[0])
	}
	if res[1].Movie != nil {
		t.Errorf("unknown should have no movie: %+v", res[1])
	}
	if res[2].Status != Downloaded || res[2].Movie != nil {
		t.Errorf("exact OpenSubtitles match should win without a hint: %+v", res[2])
	}
	// One lookup per unmatched video (shared by the similar search and the
	// hint), none for the OpenSubtitles exact match.
	if movies.calls[hashOf(t, known)] != 1 || movies.calls[hashOf(t, exactOS)] != 0 {
		t.Errorf("MovieInfo calls %v", movies.calls)
	}
}

type countingMovies struct {
	mu    sync.Mutex
	m     fakeMovies
	calls map[string]int
}

func (c *countingMovies) MovieInfo(ctx context.Context, hash string) (*napi.Movie, error) {
	c.mu.Lock()
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[hash]++
	c.mu.Unlock()
	return c.m.MovieInfo(ctx, hash)
}

// blockingSyncer waits until the run is cancelled.
type blockingSyncer struct{ started chan struct{} }

func (b *blockingSyncer) Sync(ctx context.Context, video, srt string) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestStepsAndCancel(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("v", 70*1024)
	v1 := write(t, filepath.Join(dir, "Movie (2007).mkv"), big+"1")
	v2 := write(t, filepath.Join(dir, "Other (2008).mkv"), big+"2")
	srt := "1\r\n00:00:01,000 --> 00:00:02,000\r\nx\r\n"
	movie := osdb.Subtitle{ID: "a", Language: "pl", Title: "Movie", IMDbID: 21, Files: []osdb.File{{ID: 1}}}
	fos := &fakeOS{files: map[int]string{1: srt}, byIMDb: map[int][]osdb.Subtitle{0: {movie}, 21: {movie}}}
	bs := &blockingSyncer{started: make(chan struct{})}
	cfg := config.Default()
	cfg.Jobs = 1
	var mu sync.Mutex
	var steps []string
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{Config: cfg, Client: &fakeAPI{}, OS: fos,
		NewSyncer: func(context.Context) (Syncer, error) { return bs, nil },
		OnStep: func(v string, s Step) {
			mu.Lock()
			steps = append(steps, filepath.Base(v)+":"+string(s))
			mu.Unlock()
		}}
	go func() { <-bs.started; cancel() }()
	res := r.Run(ctx, []string{v1, v2})
	got := strings.Join(steps, " ")
	want := "Movie (2007).mkv:hash Movie (2007).mkv:napi Movie (2007).mkv:os Movie (2007).mkv:os Movie (2007).mkv:download Movie (2007).mkv:sync"
	if !strings.HasPrefix(got, want) {
		t.Errorf("steps:\n%s\nwant prefix:\n%s", got, want)
	}
	if res[0].Status == Downloaded {
		t.Errorf("cancelled sync reported as downloaded: %+v", res[0])
	}
	if res[1].Status != Failed || !errors.Is(res[1].Err, context.Canceled) {
		t.Errorf("second video should be cancelled: %+v", res[1])
	}
}

func TestTitleSearchUsesOriginalTitle(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "A.Nos.Amours.1983.1080p.BluRay.x264.AAC-[YTS.MX].mp4"), strings.Repeat("v", 70*1024))
	one := func(id int) []osdb.File { return []osdb.File{{ID: id}} }
	srt := "1\r\n00:00:01,000 --> 00:00:02,000\r\nx\r\n"
	fos := &fakeOS{
		files: map[int]string{1: srt, 2: srt},
		byIMDb: map[int][]osdb.Subtitle{
			// The title search: fuzzy, English titles.
			0: {
				{ID: "xmas", Language: "pl", Title: "A Christmas Story", Year: 1983, IMDbID: 85334, Release: "Christmas Story A", Files: one(9)},
				{ID: "loves", Language: "pl", Title: "To Our Loves", Year: 1983, IMDbID: 86650, Release: "Napisy PL", Files: one(1)},
			},
			86650: {{ID: "loves", Language: "pl", Title: "To Our Loves", Year: 1983, IMDbID: 86650, Release: "A.Nos.Amours.1983.1080p.BluRay", Files: one(2)}},
		},
		features: map[int]*osdb.Feature{
			85334: {IMDbID: 85334, Title: "A Christmas Story", OriginalTitle: "A Christmas Story"},
			86650: {IMDbID: 86650, Title: "to our loves", OriginalTitle: "À nos amours", AKA: []string{"To Our Loves"}},
		},
	}
	r := &Runner{Config: config.Default(), Client: &fakeAPI{}, OS: fos,
		NewSyncer: func(context.Context) (Syncer, error) { return &shiftSyncer{}, nil }}
	res := r.Run(context.Background(), []string{v})[0]
	if res.Status != Downloaded || res.Release != "A.Nos.Amours.1983.1080p.BluRay" {
		t.Fatalf("%+v", res)
	}
	if fos.downloads != 1 {
		t.Errorf("downloads = %d (A Christmas Story must not be tried)", fos.downloads)
	}
}
