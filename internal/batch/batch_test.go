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

	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/napi"
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
	out, _ := os.ReadFile(filepath.Join(dir, "c", "Framed.srt"))
	want, _ := charmap.Windows1250.NewEncoder().String("1\r\n00:00:01,000 --> 00:00:02,000\r\nZażółć\r\ngęślą\r\n\r\n")
	if string(out) != want {
		t.Errorf("Framed.srt = %q, want %q", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "Film.PL.srt")); err != nil {
		t.Errorf("Film.PL.srt not written: %v", err)
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
	if got := OutputPath(filepath.FromSlash("/x/Film.mkv"), "ENG", ".srt", true); got != filepath.FromSlash("/x/Film.en.srt") {
		t.Fatal(got)
	}
	if got := OutputPath(filepath.FromSlash("/x/Film.2020.mkv"), "PL", ".txt", false); got != filepath.FromSlash("/x/Film.2020.txt") {
		t.Fatal(got)
	}
}
