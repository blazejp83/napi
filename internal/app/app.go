// Package app holds the logic shared by the console (napi) and windowless
// (napiw) front ends.
package app

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/blazejp83/napi/internal/batch"
	"github.com/blazejp83/napi/internal/napi"
	"github.com/blazejp83/napi/internal/pending"
)

// Version is set at build time with -ldflags "-X github.com/blazejp83/napi/internal/app.Version=...".
var Version = "dev"

// WriteCSV writes one row per result.
func WriteCSV(path string, results []batch.Result) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"video", "status", "source", "release", "lang", "format", "charset", "fps", "fps_source", "output", "message"})
	for _, r := range results {
		fps := ""
		if r.FPS > 0 {
			fps = strconv.FormatFloat(r.FPS, 'f', 3, 64)
		}
		w.Write([]string{r.Video, string(r.Status), string(r.Source), r.Release, r.Lang, r.Format, r.Charset, fps, r.FPSFrom, r.Output, Message(r)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Message is the error or warning text of a result, or for an unmatched
// video that NapiProjekt recognises, where to look for other versions.
func Message(r batch.Result) string {
	if r.Err != nil {
		return r.Err.Error()
	}
	if r.Movie != nil {
		m := "napiprojekt: " + r.Movie.Name() + " " + r.Movie.PageURL()
		if r.Warning != "" {
			m += "; " + r.Warning
		}
		return m
	}
	return r.Warning
}

// Describe is a short human-readable line for a result, without the path.
func Describe(r batch.Result) string {
	switch r.Status {
	case batch.Downloaded:
		s := r.Lang + " " + r.Format
		if filepath.Ext(r.Output) == ".srt" && r.Format != "srt" {
			s += "→srt"
		}
		if r.FPSFrom != "" {
			s += fmt.Sprintf(" @%.3f (%s)", r.FPS, r.FPSFrom)
		}
		switch r.Source {
		case batch.FromOS:
			s += " [OpenSubtitles]"
		case batch.FromOSSynced:
			s += " [OpenSubtitles, synced from " + r.Release + "]"
		case batch.FromNapiOther:
			s += " [NapiProjekt, other version synced: " + r.Release + "]"
		}
		if r.Machine {
			s += " (machine translation)"
		}
		if r.Warning != "" {
			s += " ! " + r.Warning
		}
		return s
	case batch.Skipped:
		return "already has " + filepath.Base(r.Output)
	case batch.Failed:
		return Message(r)
	case batch.NotFound:
		if r.Movie != nil {
			s := "NapiProjekt knows this movie: " + r.Movie.Name() + ", other versions at " + r.Movie.PageURL()
			if r.Warning != "" {
				s += " ! " + r.Warning
			}
			return s
		}
	}
	return r.Warning
}

// Remember records unmatched videos NapiProjekt recognises (for later
// napiprojekt: links) and forgets ones that got subtitles.
func Remember(results []batch.Result) {
	add := map[string]*napi.Movie{}
	var done []string
	for _, r := range results {
		switch {
		case r.Status == batch.NotFound && r.Movie != nil:
			add[r.Video] = r.Movie
		case r.Status == batch.Downloaded || r.Status == batch.Skipped:
			done = append(done, r.Video)
		}
	}
	pending.Default().Update(add, done)
}

// WriteRunLog saves the last run as CSV in %LOCALAPPDATA%\napi and returns
// its path ("" on failure).
func WriteRunLog(results []batch.Result) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "napi", "ostatnie.csv")
	if WriteCSV(p, results) != nil {
		return ""
	}
	return p
}

// LogError appends a line to %LOCALAPPDATA%\napi\napi.log, for failures
// the windowless front end has nowhere else to report.
func LogError(what string, err error) {
	dir, e := os.UserCacheDir()
	if e != nil {
		return
	}
	p := filepath.Join(dir, "napi", "napi.log")
	os.MkdirAll(filepath.Dir(p), 0o755)
	f, e := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if e != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: %v\n", time.Now().Format(time.RFC3339), what, err)
}
