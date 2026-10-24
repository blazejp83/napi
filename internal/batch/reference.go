package batch

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/blazejp83/napi/internal/napi"
	"github.com/blazejp83/napi/internal/subs"
	"github.com/blazejp83/napi/internal/subsync"
)

// Reference is what a subtitle file is synced against. Subtitles whose
// timing is known to match the video give a reliable, fast alignment;
// the video's audio is the fallback, and fails on films where music and
// effects drown the dialogue.
type Reference struct {
	Path  string // a subtitle file, or the video for audio
	Kind  string // file, embedded, napiprojekt, audio
	Lang  string // PL, ENG or "" for the reference's language
	Label string // where it came from, e.g. "Subs\4_English.srt"

	cleanup func()
}

// Audio reports whether the reference is the video's sound.
func (r Reference) Audio() bool { return r.Kind == "audio" }

// TrackExtractor reads embedded subtitle tracks; *subsync.Tools implements it.
type TrackExtractor interface {
	TextTracks(ctx context.Context, video string) ([]subsync.Track, error)
	ExtractTrack(ctx context.Context, video string, track subsync.Track, out string) error
}

// findReference picks the best timing reference for syncing subtitles in
// lang to video, from most to least reliable:
//  1. subtitles already next to the video in another language (including a
//     release's Subs folder), except ones napi itself synced from audio;
//  2. an exact NapiProjekt match in another language (hash is the video's);
//  3. an embedded text subtitle track;
//  4. the audio.
//
// References are cleaned of SDH additions (sound descriptions, music lines,
// speaker labels), which otherwise derail the alignment. n is the number of
// lines being synced: references with less than half as many are rejected
// (forced-only subtitles), and the closest count wins. exclude is a file
// that must not be used (the one being re-synced).
func (r *Runner) findReference(ctx context.Context, video, hash, lang string, n int, exclude string) Reference {
	minCues := n / 2
	if f, l, cues := referenceFile(video, lang, n, exclude); f != "" {
		rel, _ := filepath.Rel(filepath.Dir(video), f)
		if p, ok := writeCues(cues); ok {
			return Reference{Path: p, Kind: "file", Lang: l, Label: rel, cleanup: func() { os.Remove(p) }}
		}
	}
	if hash == "" {
		hash, _ = napi.FileHash(video)
	}
	if hash != "" && r.Client != nil {
		for _, other := range []string{"ENG", "PL"} {
			if other == lang {
				continue
			}
			sub, err := r.download(ctx, hash, other)
			if err != nil {
				continue
			}
			if p, ok := writeReference(video, sub.Content, r.Config.DefaultFPS, minCues); ok {
				return Reference{Path: p, Kind: "napiprojekt", Lang: other, Label: "NapiProjekt",
					cleanup: func() { os.Remove(p) }}
			}
		}
	}
	if s, err := r.getSyncer(ctx); err == nil {
		if tx, ok := s.(TrackExtractor); ok {
			if ref, ok := embeddedReference(ctx, tx, video, minCues); ok {
				return ref
			}
		}
	}
	return Reference{Path: video, Kind: "audio", Label: "dźwięk filmu"}
}

// referenceFile finds subtitles next to video in a language other than lang:
// tagged or recognised files of the video (Film.eng.srt), and a release's
// Subs folder (Subs\4_English.srt, or Subs\<video name>\… for series). It
// returns the file, its language and its cues without SDH additions. English
// wins, then the line count closest to n.
func referenceFile(video, lang string, n int, exclude string) (string, string, []subs.Cue) {
	dir := filepath.Dir(video)
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(video), filepath.Ext(video)))
	var cands []string
	add := func(d string, own func(name string) bool) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if !e.IsDir() && refExts[filepath.Ext(n)] && own(n) {
				cands = append(cands, filepath.Join(d, e.Name()))
			}
		}
	}
	add(dir, func(n string) bool { return strings.HasPrefix(n, base+".") })
	if sd := findDir(dir, "subs"); sd != "" {
		if vd := findDir(sd, base); vd != "" {
			add(vd, func(string) bool { return true })
		} else if videosIn(dir) == 1 {
			add(sd, func(string) bool { return true })
		}
	}
	type cand struct {
		path, lang string
		cues       []subs.Cue
	}
	var ok []cand
	for _, p := range cands {
		if exclude != "" && strings.EqualFold(p, exclude) || audioSynced(p) {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		l := tagLang(strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' }))
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text, _ := subs.Decode(raw)
		if l == "" {
			l = subs.DetectLanguage(text)
		}
		// The target language is being replaced and may be the bad one;
		// an unrecognised language could be it too.
		if l == "" || l == lang {
			continue
		}
		f := subs.Detect(text)
		if f != subs.SubRip {
			continue
		}
		cues, err := subs.Parse(text, f, 0)
		if err != nil {
			continue
		}
		cues = subs.StripSDH(cues)
		if len(cues) < n/2 {
			continue
		}
		ok = append(ok, cand{p, l, cues})
	}
	sort.SliceStable(ok, func(i, j int) bool {
		if (ok[i].lang == "ENG") != (ok[j].lang == "ENG") {
			return ok[i].lang == "ENG"
		}
		return abs(len(ok[i].cues)-n) < abs(len(ok[j].cues)-n)
	})
	if len(ok) == 0 {
		return "", "", nil
	}
	return ok[0].path, ok[0].lang, ok[0].cues
}

// writeCues saves cues as a temporary SRT file for alass.
func writeCues(cues []subs.Cue) (string, bool) {
	f, err := os.CreateTemp("", "napi-ref-*.srt")
	if err != nil {
		return "", false
	}
	_, err = f.WriteString(subs.WriteSRT(cues))
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return "", false
	}
	return f.Name(), true
}

var refExts = map[string]bool{".srt": true}

func findDir(parent, name string) string {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(parent, e.Name())
		}
	}
	return ""
}

func videosIn(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".mkv", ".mp4", ".avi", ".m4v", ".mov", ".wmv", ".ts", ".m2ts", ".mpg", ".mpeg", ".webm":
			if !e.IsDir() && !strings.Contains(strings.ToLower(e.Name()), "sample") {
				n++
			}
		}
	}
	return n
}

// writeReference converts downloaded subtitles to a cleaned temporary SRT file.
func writeReference(video string, content []byte, defaultFPS float64, minCues int) (string, bool) {
	_, cues, err := toCues(video, content, defaultFPS)
	if err != nil {
		return "", false
	}
	if cues = subs.StripSDH(cues); len(cues) < minCues {
		return "", false
	}
	return writeCues(cues)
}

func embeddedReference(ctx context.Context, tx TrackExtractor, video string, minCues int) (Reference, bool) {
	tracks, err := tx.TextTracks(ctx, video)
	if err != nil || len(tracks) == 0 {
		return Reference{}, false
	}
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].Lang == "eng" && tracks[j].Lang != "eng" })
	f, err := os.CreateTemp("", "napi-ref-*.srt")
	if err != nil {
		return Reference{}, false
	}
	f.Close()
	if err := tx.ExtractTrack(ctx, video, tracks[0], f.Name()); err != nil {
		os.Remove(f.Name())
		return Reference{}, false
	}
	if raw, err := os.ReadFile(f.Name()); err == nil {
		text, _ := subs.Decode(raw)
		if cues, err := subs.Parse(text, subs.SubRip, 0); err == nil && len(subs.StripSDH(cues)) >= minCues {
			os.WriteFile(f.Name(), []byte(subs.WriteSRT(subs.StripSDH(cues))), 0o644)
			lang := map[string]string{"eng": "ENG", "pol": "PL"}[tracks[0].Lang]
			p := f.Name()
			return Reference{Path: p, Kind: "embedded", Lang: lang, Label: "napisy wbudowane w plik",
				cleanup: func() { os.Remove(p) }}, true
		}
	}
	os.Remove(f.Name())
	return Reference{}, false
}

// syncLog remembers the subtitle files napi synced, in
// %LOCALAPPDATA%\napi\synced.json: whether they were synced to the audio
// (which may be wrong, so they are never used as a reference) and where the
// pre-sync original is kept (a re-sync starts from it, because alass can
// settle on a wrong alignment when started from badly synced timing). An
// entry only counts while the file is unchanged since napi wrote it.
var syncLog = struct {
	sync.Mutex
	dir string
}{dir: func() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "napi")
}()}

type syncEntry struct {
	Path     string `json:"path"`
	Hash     string `json:"hash"` // SHA-1 of the file as napi wrote it
	Audio    bool   `json:"audio"`
	Original string `json:"original"` // file in originals\ with the pre-sync subtitles
}

func readSyncLog() map[string]syncEntry {
	m := map[string]syncEntry{}
	if syncLog.dir != "" {
		if b, err := os.ReadFile(filepath.Join(syncLog.dir, "synced.json")); err == nil {
			json.Unmarshal(b, &m)
		}
	}
	return m
}

// syncedEntry returns the log entry for p if p is unchanged since napi wrote it.
func syncedEntry(p string) (syncEntry, bool) {
	syncLog.Lock()
	defer syncLog.Unlock()
	e, ok := readSyncLog()[strings.ToLower(p)]
	if !ok {
		return e, false
	}
	h, err := fileHash(p)
	return e, err == nil && h == e.Hash
}

func fileHash(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:]), nil
}

func audioSynced(p string) bool {
	e, ok := syncedEntry(p)
	return ok && e.Audio
}

// originalOf returns the pre-sync subtitles of a file napi synced, if it is
// unchanged since.
func originalOf(p string) ([]byte, bool) {
	e, ok := syncedEntry(p)
	if !ok || e.Original == "" {
		return nil, false
	}
	b, err := os.ReadFile(e.Original)
	return b, err == nil
}

// noteSynced records out as synced (from audio or not) with original as its
// pre-sync content.
func noteSynced(out string, audio bool, original []byte) error {
	syncLog.Lock()
	defer syncLog.Unlock()
	if syncLog.dir == "" {
		return errors.New("no cache folder")
	}
	h, err := fileHash(out)
	if err != nil {
		return err
	}
	m := readSyncLog()
	key := strings.ToLower(out)
	e := syncEntry{Path: out, Hash: h, Audio: audio}
	if len(original) > 0 {
		od := filepath.Join(syncLog.dir, "originals")
		os.MkdirAll(od, 0o755)
		sum := sha1.Sum([]byte(key))
		e.Original = filepath.Join(od, hex.EncodeToString(sum[:])+".srt")
		if err := os.WriteFile(e.Original, original, 0o644); err != nil {
			e.Original = ""
		}
	}
	m[key] = e
	for k, v := range m { // forget files that are gone
		if _, err := os.Stat(v.Path); err != nil {
			if v.Original != "" {
				os.Remove(v.Original)
			}
			delete(m, k)
		}
	}
	b, _ := json.Marshal(m)
	return os.WriteFile(filepath.Join(syncLog.dir, "synced.json"), b, 0o644)
}
