// Package app holds the logic shared by the console (napi) and windowless
// (napiw) front ends.
package app

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/blaze/napi/internal/batch"
)

// Version is set at build time with -ldflags "-X github.com/blaze/napi/internal/app.Version=...".
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
