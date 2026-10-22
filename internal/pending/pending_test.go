package pending

import (
	"path/filepath"
	"testing"

	"github.com/blazejp83/napi/internal/napi"
)

func TestStore(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "pending.json")}
	film := &napi.Movie{ID: "f", Title: "Film"}
	e3 := &napi.Movie{ID: "s", Title: "Show", Series: true, Season: 1, Episode: 3}
	e4 := &napi.Movie{ID: "s", Title: "Show", Series: true, Season: 1, Episode: 4}
	if err := s.Update(map[string]*napi.Movie{`Z:\Film.mkv`: film, `Z:\S01E03.mkv`: e3, `Z:\S01E04.mkv`: e4}, nil); err != nil {
		t.Fatal(err)
	}
	if m := s.Match(e4); len(m) != 1 || m[0].Video != `Z:\S01E04.mkv` {
		t.Fatalf("episode match %+v", m)
	}
	if m := s.Match(&napi.Movie{ID: "f"}); len(m) != 1 || m[0].Video != `Z:\Film.mkv` {
		t.Fatalf("movie match %+v", m)
	}
	if m := s.Match(nil); len(m) != 3 {
		t.Fatalf("recent entries %+v", m)
	}
	s.Update(nil, []string{`z:\film.mkv`})
	if m := s.Match(&napi.Movie{ID: "f"}); len(m) != 0 {
		t.Fatalf("done video still pending: %+v", m)
	}
}
