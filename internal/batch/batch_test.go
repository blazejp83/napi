package batch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/blazejp83/napi/internal/config"
	"github.com/blazejp83/napi/internal/napi"
)

// fakeAPI serves subtitles keyed by "hash/lang" and fails the first call for
// hashes in flaky.
type fakeAPI struct {
	mu    sync.Mutex
	subs  map[string]string
	flaky map[string]bool
	calls []string
}

func (f *fakeAPI) Download(_ context.Context, hash, lang string) (*napi.Subtitles, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, hash+"/"+lang)
	if f.flaky[hash] {
		delete(f.flaky, hash)
		return nil, &napi.TemporaryError{Err: errors.New("connection reset")}
	}
	s, ok := f.subs[hash+"/"+lang]
	if !ok {
		return nil, napi.ErrNotFound
	}
	return &napi.Subtitles{Hash: hash, Lang: lang, Content: []byte(s)}, nil
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func hashOf(t *testing.T, p string) string {
	h, err := napi.FileHash(p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	plOnly := write(t, filepath.Join(dir, "a", "Film.PL.avi"), "video pl")
	engOnly := write(t, filepath.Join(dir, "a", "Eng.mkv"), "video eng")
	write(t, filepath.Join(dir, "b", "deep", "Missing.mp4"), "video missing")
	hasSubs := write(t, filepath.Join(dir, "b", "Done.mkv"), "video done")
	write(t, filepath.Join(dir, "b", "Done.pl.srt"), "old")
	write(t, filepath.Join(dir, "b", "sample.mkv"), "sample clip")
	write(t, filepath.Join(dir, "b", "notes.txt"), "not a video")

	// A real 25 fps MKV so MicroDVD frames convert with the video's own rate.
	fixture, err := os.ReadFile("../media/testdata/fps_25.mkv")
	if err != nil {
		t.Fatal(err)
	}
	framed := write(t, filepath.Join(dir, "c", "Framed.mkv"), string(fixture))

	cp1250, _ := charmap.Windows1250.NewEncoder().String("{25}{50}Zażółć|gęślą")
	api := &fakeAPI{
		subs: map[string]string{
			hashOf(t, plOnly) + "/PL":   "1\r\n00:00:01,000 --> 00:00:02,000\r\nCześć\r\n",
			hashOf(t, engOnly) + "/ENG": "[10][20]Hello",
			hashOf(t, framed) + "/PL":   cp1250,
		},
		flaky: map[string]bool{hashOf(t, plOnly): true},
	}

	cfg := config.Default()
	cfg.Languages = []string{"PL", "ENG"}
	cfg.Encoding = "cp1250"
	cfg.Jobs = 3
	videos, err := Collect([]string{filepath.Join(dir, "a"), filepath.Join(dir, "b"), framed}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 5 {
		t.Fatalf("collected %v", videos)
	}
	r := &Runner{Config: cfg, Client: api}
	results := r.Run(context.Background(), videos)

	byName := map[string]Result{}
	for _, res := range results {
		byName[filepath.Base(res.Video)] = res
	}
	expect := map[string]Status{"Film.PL.avi": Downloaded, "Eng.mkv": Downloaded, "Missing.mp4": NotFound,
		"Done.mkv": Skipped, "Framed.mkv": Downloaded}
	for name, st := range expect {
		if got := byName[name]; got.Status != st {
			t.Errorf("%s: status %s (%v), want %s", name, got.Status, got.Err, st)
		}
	}
	if byName["Eng.mkv"].Lang != "ENG" {
		t.Errorf("Eng.mkv should fall back to ENG, got %q", byName["Eng.mkv"].Lang)
	}
	f := byName["Framed.mkv"]
	if f.FPS != 25 || f.FPSFrom != "mkv" || f.Charset != "windows-1250" {
		t.Errorf("Framed.mkv: fps %v from %s, charset %s", f.FPS, f.FPSFrom, f.Charset)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "c", "Framed.pol.srt"))
	want, _ := charmap.Windows1250.NewEncoder().String("1\r\n00:00:01,000 --> 00:00:02,000\r\nZażółć\r\ngęślą\r\n\r\n")
	if string(out) != want {
		t.Errorf("Framed.pol.srt = %q, want %q", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "Film.PL.pol.srt")); err != nil {
		t.Errorf("Film.PL.pol.srt not written: %v", err)
	}
	if old, _ := os.ReadFile(filepath.Join(dir, "b", "Done.pl.srt")); string(old) != "old" {
		t.Error("existing subtitles were overwritten")
	}
	if s := Summarize(results); s.ExitCode() != 2 || s.Downloaded != 3 {
		t.Errorf("summary %+v exit %d", s, s.ExitCode())
	}
	// The flaky hash was retried; Done.mkv never reached the API.
	calls := strings.Join(api.calls, " ")
	if strings.Count(calls, hashOf(t, plOnly)+"/PL") != 2 || strings.Contains(calls, hashOf(t, hasSubs)) {
		t.Errorf("unexpected API calls: %s", calls)
	}
}

func TestOutputPath(t *testing.T) {
	for _, tc := range []struct{ lang, ext, naming, want string }{
		{"PL", ".srt", "iso639-2", "/x/Film.pol.srt"},
		{"ENG", ".srt", "iso639-2", "/x/Film.eng.srt"},
		{"ENG", ".srt", "iso639-1", "/x/Film.en.srt"},
		{"PL", ".txt", "none", "/x/Film.txt"},
	} {
		if got := OutputPath(filepath.FromSlash("/x/Film.mkv"), tc.lang, tc.ext, tc.naming); got != filepath.FromSlash(tc.want) {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}

func TestExistingFor(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), "v")
	pl := strings.Repeat("1\n00:00:01,000 --> 00:00:02,000\nNie wiem, czy to jest dobry pomysł.\n\n", 10)
	en := strings.Repeat("1\n00:00:01,000 --> 00:00:02,000\nI don't know what you want from me.\n\n", 10)
	write(t, filepath.Join(dir, "Film.srt"), en) // untagged, English text
	write(t, filepath.Join(dir, "Film.PL.srt"), pl)
	write(t, filepath.Join(dir, "Film.forced.txt"), "1\n00:00:01,000 --> 00:00:02,000\n?\n")
	write(t, filepath.Join(dir, "Film.pol.srt.bak"), pl)
	write(t, filepath.Join(dir, "Other.pol.srt"), pl)
	names := func(ps []string) string {
		var n []string
		for _, p := range ps {
			n = append(n, filepath.Base(p))
		}
		return strings.Join(n, ",")
	}
	if got := names(ExistingFor(v, "PL", true)); got != "Film.PL.srt,Film.forced.txt" {
		t.Errorf("PL: %s", got)
	}
	if got := names(ExistingFor(v, "ENG", false)); got != "Film.srt" {
		t.Errorf("ENG: %s", got)
	}
}

func TestReplaceKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), "video replace")
	write(t, filepath.Join(dir, "Film.pl.srt"), "old pl")
	write(t, filepath.Join(dir, "Film.eng.srt"), "english stays")
	srt := "1\r\n00:00:01,000 --> 00:00:02,000\r\nNowe\r\n"
	cfg := config.Default()
	cfg.Encoding = "utf8"
	run := func(overwrite bool) Result {
		cfg.Overwrite = overwrite
		r := &Runner{Config: cfg, Client: &fakeAPI{subs: map[string]string{hashOf(t, v) + "/PL": srt}}}
		return r.Run(context.Background(), []string{v})[0]
	}
	if res := run(false); res.Status != Skipped || filepath.Base(res.Output) != "Film.pl.srt" {
		t.Fatalf("without overwrite: %+v", res)
	}
	if res := run(true); res.Status != Downloaded || filepath.Base(res.Output) != "Film.pol.srt" {
		t.Fatalf("with overwrite: %+v", res)
	}
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join(dir, n)); return string(b) }
	if read("Film.pl.srt.bak") != "old pl" || read("Film.eng.srt") != "english stays" || !strings.Contains(read("Film.pol.srt"), "Nowe") {
		t.Fatalf("files: bak=%q eng=%q new=%q", read("Film.pl.srt.bak"), read("Film.eng.srt"), read("Film.pol.srt"))
	}
	if _, err := os.Stat(filepath.Join(dir, "Film.pl.srt")); err == nil {
		t.Fatal("old Polish subtitles left in place next to the new ones")
	}
}

func TestEnglishOnlyIgnoresPolish(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), "video eng only")
	write(t, filepath.Join(dir, "Film.pol.srt"), "polskie")
	cfg := config.Default()
	cfg.Languages = []string{"ENG"}
	r := &Runner{Config: cfg, Client: &fakeAPI{subs: map[string]string{hashOf(t, v) + "/ENG": "[10][20]Hello"}}}
	res := r.Run(context.Background(), []string{v})[0]
	if res.Status != Downloaded || filepath.Base(res.Output) != "Film.eng.srt" {
		t.Fatalf("%+v", res)
	}
}

func TestFallbackLanguageAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), "video fallback")
	write(t, filepath.Join(dir, "Film.eng.srt"), "english")
	cfg := config.Default()
	cfg.Languages = []string{"PL", "ENG"}
	api := &fakeAPI{}
	res := (&Runner{Config: cfg, Client: api}).Run(context.Background(), []string{v})[0]
	if res.Status != Skipped || res.Lang != "ENG" || len(api.calls) != 1 {
		t.Fatalf("%+v calls %v", res, api.calls)
	}
}
