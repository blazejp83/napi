// Package pending remembers videos left without subtitles that NapiProjekt
// recognises, so a napiprojekt: link clicked later on the movie's page can be
// synced to the right file.
package pending

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blaze/napi/internal/napi"
)

const maxAge = 14 * 24 * time.Hour

type Entry struct {
	Video   string    `json:"video"`
	MovieID string    `json:"movieId"`
	Name    string    `json:"name"`
	Season  int       `json:"season,omitempty"`
	Episode int       `json:"episode,omitempty"`
	Added   time.Time `json:"added"`
}

// Store is the list kept in %LOCALAPPDATA%\napi\pending.json.
type Store struct{ Path string }

func Default() *Store {
	dir, err := os.UserCacheDir()
	if err != nil {
		return &Store{}
	}
	return &Store{Path: filepath.Join(dir, "napi", "pending.json")}
}

// Load returns the entries, newest first, without expired ones.
func (s *Store) Load() []Entry {
	var all []Entry
	if s.Path == "" {
		return nil
	}
	if b, err := os.ReadFile(s.Path); err == nil {
		json.Unmarshal(b, &all)
	}
	var out []Entry
	for _, e := range all {
		if time.Since(e.Added) < maxAge {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Added.After(out[j].Added) })
	return out
}

func (s *Store) save(entries []Entry) error {
	if s.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Update adds videos waiting for a manual pick and removes ones that got
// subtitles since.
func (s *Store) Update(add map[string]*napi.Movie, done []string) error {
	if len(add) == 0 && len(done) == 0 {
		return nil
	}
	drop := map[string]bool{}
	for _, v := range done {
		drop[strings.ToLower(v)] = true
	}
	for v := range add {
		drop[strings.ToLower(v)] = true // replaced by the fresh entry
	}
	var out []Entry
	for _, e := range s.Load() {
		if !drop[strings.ToLower(e.Video)] {
			out = append(out, e)
		}
	}
	now := time.Now()
	for v, m := range add {
		out = append(out, Entry{Video: v, MovieID: m.ID, Name: m.Name(), Season: m.Season, Episode: m.Episode, Added: now})
	}
	return s.save(out)
}

// Match returns the entries a link's movie belongs to: same movie and, for
// series, the same episode. Without movie information it returns the
// entries added in the last two hours.
func (s *Store) Match(m *napi.Movie) []Entry {
	var out []Entry
	for _, e := range s.Load() {
		switch {
		case m == nil:
			if time.Since(e.Added) < 2*time.Hour {
				out = append(out, e)
			}
		case e.MovieID == m.ID && (!m.Series || m.Episode == 0 || (e.Season == m.Season && e.Episode == m.Episode)):
			out = append(out, e)
		}
	}
	return out
}
