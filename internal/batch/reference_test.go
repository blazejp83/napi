package batch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blazejp83/napi/internal/config"
)

func TestMain(m *testing.M) {
	// Keep the audio-synced list out of the real user cache.
	dir, _ := os.MkdirTemp("", "napi-test-*")
	syncLog.dir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// srtOf builds n English or Polish lines.
func srtOf(n int, lang string) string {
	line := "I don't know what you want from me, is that the plan?"
	if lang == "PL" {
		line = "Nie wiem, czego ode mnie chcesz, czy to jest plan?"
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		s := i * 3
		b.WriteString(strings.Join([]string{
			itoa(i + 1),
			ts(s) + " --> " + ts(s+2),
			line, "", "",
		}, "\r\n"))
	}
	return b.String()
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + func() string { return fmtInt(n) }())
}
func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
func ts(sec int) string {
	two := func(n int) string {
		s := fmtInt(n)
		if len(s) < 2 {
			s = "0" + s
		}
		return s
	}
	return two(sec/3600) + ":" + two(sec/60%60) + ":" + two(sec%60) + ",000"
}

func TestReferenceFile(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.2020.mkv"), "v")
	write(t, filepath.Join(dir, "Film.2020.pol.srt"), srtOf(100, "PL"))            // target language: never a reference
	write(t, filepath.Join(dir, "Film.2020.eng.srt"), srtOf(80, "ENG"))            // audio-synced below: skipped
	write(t, filepath.Join(dir, "Subs", "5_English.srt"), srtOf(10, "ENG"))        // forced: too few lines
	sdh := write(t, filepath.Join(dir, "Subs", "4_English.srt"), srtOf(90, "ENG")) // the one
	if err := noteSynced(filepath.Join(dir, "Film.2020.eng.srt"), true, nil); err != nil {
		t.Fatal(err)
	}
	got, lang, _ := referenceFile(v, "PL", 100, "")
	if got != sdh || lang != "ENG" {
		t.Fatalf("got %q %q, want Subs\\4_English.srt", got, lang)
	}
	// A second video in the folder makes a loose Subs folder ambiguous.
	write(t, filepath.Join(dir, "Other.2021.mkv"), "o")
	if got, _, _ := referenceFile(v, "PL", 100, ""); got != "" {
		t.Fatalf("ambiguous Subs folder used: %q", got)
	}
	// Re-syncing English: Polish is a fine reference then.
	got, lang, _ = referenceFile(v, "ENG", 100, "")
	if filepath.Base(got) != "Film.2020.pol.srt" || lang != "PL" {
		t.Fatalf("for ENG got %q %q", got, lang)
	}
}

// refSyncer records what it was asked to sync against (references are
// temporary files, so their content is kept too).
type refSyncer struct{ refs, content []string }

func (s *refSyncer) Sync(_ context.Context, ref, srt string) (string, error) {
	b, _ := os.ReadFile(ref)
	s.refs, s.content = append(s.refs, ref), append(s.content, string(b))
	return srt, nil
}

func TestSyncUsesReference(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), strings.Repeat("v", 70*1024))
	eng := write(t, filepath.Join(dir, "Film.eng.srt"), srtOf(60, "ENG"))
	pol := write(t, filepath.Join(dir, "Film.pol.srt"), srtOf(60, "PL"))
	s := &refSyncer{}
	cfg := config.Default()
	r := &Runner{Config: cfg, Client: &fakeAPI{}, NewSyncer: func(context.Context) (Syncer, error) { return s, nil }}
	res := r.SyncFiles(context.Background(), []string{pol})[0]
	if res.Status != Downloaded || res.SyncAudio || res.SyncRef != "Film.eng.srt" || !strings.Contains(s.content[0], "what you want") {
		t.Fatalf("%+v refs %v", res, s.refs)
	}
	// The re-synced Polish file is not recorded as audio-synced.
	if audioSynced(pol) {
		t.Error("reference-synced file marked as audio-synced")
	}

	// No reference anywhere: the audio, and the output is remembered as such.
	os.Remove(eng)
	res = r.SyncFiles(context.Background(), []string{pol})[0]
	if s.refs[1] != v || !res.SyncAudio || !audioSynced(pol) {
		t.Fatalf("%+v refs %v", res, s.refs)
	}
}

func TestReferenceFromNapiProjekt(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), strings.Repeat("v", 70*1024))
	api := &fakeAPI{subs: map[string]string{hashOf(t, v) + "/ENG": srtOf(60, "ENG")}}
	s := &refSyncer{}
	r := &Runner{Config: config.Default(), Client: api, NewSyncer: func(context.Context) (Syncer, error) { return s, nil }}
	ref := r.findReference(context.Background(), v, "", "PL", 60, "")
	defer ref.cleanup()
	if ref.Kind != "napiprojekt" || ref.Lang != "ENG" {
		t.Fatalf("%+v", ref)
	}
	if b, _ := os.ReadFile(ref.Path); !strings.Contains(string(b), "what you want") {
		t.Fatalf("reference file: %q", b)
	}
}

func TestReferencePrefersClosestAndCleansSDH(t *testing.T) {
	dir := t.TempDir()
	v := write(t, filepath.Join(dir, "Film.mkv"), "v")
	plain := srtOf(100, "ENG")
	// SDH: the same dialogue plus 60 sound-description lines.
	sdh := plain + strings.Replace(srtOf(60, "ENG"), "I don't know what you want from me, is that the plan?", "[Dramatic music plays]", -1)
	write(t, filepath.Join(dir, "Subs", "5_English.srt"), sdh)
	write(t, filepath.Join(dir, "Subs", "4_English.srt"), srtOf(130, "ENG"))
	got, _, cues := referenceFile(v, "PL", 100, "")
	if filepath.Base(got) != "5_English.srt" || len(cues) != 100 {
		t.Fatalf("got %s with %d cues; want the SDH file cleaned to 100 lines", filepath.Base(got), len(cues))
	}
}

func TestResyncStartsFromOriginal(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Film.mkv"), "v")
	original := srtOf(40, "PL")
	pol := write(t, filepath.Join(dir, "Film.pol.srt"), original)
	s := &refSyncer{}
	r := &Runner{Config: config.Default(), Client: &fakeAPI{}, NewSyncer: func(context.Context) (Syncer, error) { return s, nil }}
	r.Config.Encoding = "utf8"
	// First sync records the original; then the file is "damaged" by a
	// shifted sync result.
	r.SyncFiles(context.Background(), []string{pol})
	damaged := strings.Replace(original, "00:00:00,000", "00:00:01,500", 1)
	os.WriteFile(pol, []byte(damaged), 0o644)
	noteSynced(pol, true, []byte(original))
	r.SyncFiles(context.Background(), []string{pol})
	got, _ := os.ReadFile(pol)
	if strings.Contains(string(got), "00:00:01,500") {
		t.Fatal("re-sync started from the damaged file instead of the original")
	}
	// Edited by the user since: the file itself is used.
	os.WriteFile(pol, []byte(damaged), 0o644)
	r.SyncFiles(context.Background(), []string{pol})
	if got, _ := os.ReadFile(pol); !strings.Contains(string(got), "00:00:01,500") {
		t.Fatal("user-edited file was replaced by the stored original")
	}
}
