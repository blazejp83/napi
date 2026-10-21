//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/blaze/napi/internal/app"
	"github.com/blaze/napi/internal/batch"
	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/pending"
)

// PanelState is everything the corner card shows.
type PanelState struct {
	Phase   string `json:"phase"` // "running" or "done"
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Busy    bool   `json:"busy"` // indeterminate progress (one long step)
	Kind    string `json:"kind"` // done: "ok", "partial", "none", "error", "cancelled"
	Message string `json:"message"`

	Results []ResultView `json:"results"`
	Pages   []PageView   `json:"pages"`
	Output  string       `json:"output"`    // single produced file, for "Pokaż plik"
	Log     string       `json:"log"`       // CSV report path
	Close   int          `json:"autoClose"` // seconds until the card closes itself, 0 = stays
}

type ResultView struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, synced, exists, not-found, error
	Text   string `json:"text"`
	Output string `json:"output"`
	Page   string `json:"page"`
}

type PageView struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

var stepText = map[batch.Step]string{
	batch.StepHash:     "Czytam plik",
	batch.StepNapi:     "Szukam w NapiProjekt",
	batch.StepOS:       "Szukam w OpenSubtitles",
	batch.StepDownload: "Pobieram napisy",
	batch.StepSync:     "Synchronizuję z dźwiękiem filmu",
}

// BatchJob processes videos and folders from the Explorer menu. more is
// polled after each round for paths queued by later menu launches.
func BatchJob(paths []string, more func() []string) func(ctx context.Context, a *App) {
	return func(ctx context.Context, a *App) {
		var all []batch.Result
		for round := 0; len(paths) > 0; round++ {
			videos, err := batch.Collect(paths, a.cfg)
			if err != nil {
				a.finishError(err.Error())
				return
			}
			if len(videos) == 0 && len(all) == 0 {
				a.finish(nil, "none", "Nie znaleziono plików wideo.", "")
				return
			}
			base := len(all)
			a.setState(func(s *PanelState) {
				s.Phase, s.Total, s.Done = "running", base+len(videos), base
				s.Title, s.Detail, s.Busy = "Szukam napisów…", "", false
			})
			r := batch.NewRunner(a.cfg)
			r.OnStep = func(video string, step batch.Step) {
				a.setState(func(s *PanelState) {
					s.Title = stepText[step] + "…"
					s.Detail = filepath.Base(video)
					s.Busy = step == batch.StepSync && s.Total == 1
				})
			}
			r.Progress = func(done, total int, res batch.Result) {
				a.setState(func(s *PanelState) { s.Done = base + done })
			}
			all = append(all, r.Run(ctx, videos)...)
			if ctx.Err() != nil {
				break
			}
			paths = more()
		}
		app.Remember(all)
		a.finishBatch(ctx, all)
	}
}

func (a *App) finishBatch(ctx context.Context, results []batch.Result) {
	s := batch.Summarize(results)
	logPath := app.WriteRunLog(results)
	cancelled := 0
	for _, r := range results {
		if errors.Is(r.Err, context.Canceled) {
			cancelled++
		}
	}
	kind, msg := "ok", ""
	switch {
	case cancelled > 0:
		kind, msg = "cancelled", fmt.Sprintf("Przerwano. Pobrano %d z %d.", s.Downloaded, len(results))
	case len(results) == 1:
		kind, msg = singleMessage(results[0])
	case s.Failed > 0:
		kind = "error"
	case s.NotFound > 0 && s.Downloaded+s.Skipped > 0:
		kind = "partial"
	case s.NotFound > 0:
		kind = "none"
	}
	if msg == "" {
		msg = summaryText(s)
	}
	output := ""
	if len(results) == 1 && results[0].Output != "" {
		output = results[0].Output
	}
	a.finish(results, kind, msg, output)
	a.setState(func(st *PanelState) { st.Log = logPath })
	a.autoClose(ctx, kind, results)
}

func singleMessage(r batch.Result) (string, string) {
	name := filepath.Base(r.Video)
	switch r.Status {
	case batch.Downloaded:
		switch r.Source {
		case batch.FromOSSynced, batch.FromNapiOther:
			return "ok", "Napisy z innej wersji, zsynchronizowane – sprawdź, czy pasują."
		case batch.FromOS:
			return "ok", "Pobrano napisy z OpenSubtitles (" + r.Lang + ")."
		}
		return "ok", "Pobrano napisy z NapiProjekt (" + r.Lang + ")."
	case batch.Skipped:
		return "ok", "Napisy już są: " + filepath.Base(r.Output)
	case batch.NotFound:
		if r.Movie != nil {
			return "none", "Brak napisów do tej wersji. NapiProjekt zna film " + r.Movie.Name() +
				" – kliknij napisy na jego stronie, a dopasuję je automatycznie."
		}
		return "none", "Nie znaleziono napisów dla " + name + "."
	}
	return "error", app.Message(r)
}

func summaryText(s batch.Summary) string {
	t := fmt.Sprintf("Pobrano %d", s.Downloaded)
	if s.Synced > 0 {
		t += fmt.Sprintf(" (zsynchronizowane: %d)", s.Synced)
	}
	if s.Skipped > 0 {
		t += fmt.Sprintf(", już były %d", s.Skipped)
	}
	if s.NotFound > 0 {
		t += fmt.Sprintf(", brak %d", s.NotFound)
	}
	if s.Failed > 0 {
		t += fmt.Sprintf(", błędy %d", s.Failed)
	}
	return t + "."
}

func (a *App) finish(results []batch.Result, kind, msg, output string) {
	views := make([]ResultView, 0, len(results))
	var pages []PageView
	seen := map[string]bool{}
	for _, r := range results {
		v := ResultView{Name: filepath.Base(r.Video), Status: string(r.Status), Output: r.Output, Text: resultText(r)}
		if r.Status == batch.Downloaded && (r.Source == batch.FromOSSynced || r.Source == batch.FromNapiOther) {
			v.Status = "synced"
		}
		if r.Status == batch.NotFound && r.Movie != nil {
			v.Page = r.Movie.PageURL()
			v.Text = "NapiProjekt zna film: " + r.Movie.Name()
			if !seen[r.Movie.ID] {
				seen[r.Movie.ID] = true
				pages = append(pages, PageView{Name: r.Movie.Name(), URL: r.Movie.PageURL()})
			}
		}
		views = append(views, v)
	}
	// Problems first, then synced, then the rest.
	rank := map[string]int{"error": 0, "not-found": 1, "synced": 2, "ok": 3, "exists": 4}
	sort.SliceStable(views, func(i, j int) bool { return rank[views[i].Status] < rank[views[j].Status] })
	a.setState(func(s *PanelState) {
		s.Phase, s.Kind, s.Message, s.Busy = "done", kind, msg, false
		s.Title, s.Detail = doneTitle(kind), ""
		s.Results, s.Pages, s.Output = views, pages, output
		s.Done = s.Total
	})
}

func doneTitle(kind string) string {
	switch kind {
	case "ok":
		return "Gotowe"
	case "partial":
		return "Gotowe, ale nie wszystko"
	case "none":
		return "Nie znaleziono napisów"
	case "cancelled":
		return "Przerwano"
	}
	return "Wystąpił błąd"
}

func (a *App) finishError(msg string) {
	a.setState(func(s *PanelState) {
		s.Phase, s.Kind, s.Title, s.Message, s.Busy = "done", "error", "Wystąpił błąd", msg, false
	})
}

// autoClose closes the card after a clean success; anything that needs
// attention stays until the user closes it.
func (a *App) autoClose(ctx context.Context, kind string, results []batch.Result) {
	if kind != "ok" {
		return
	}
	for _, r := range results {
		if r.Warning != "" || r.Source == batch.FromOSSynced || r.Source == batch.FromNapiOther {
			return // synced subtitles deserve a look at the message
		}
	}
	const secs = 6
	a.setState(func(s *PanelState) { s.Close = secs })
}

// LinkJob downloads the subtitles behind a napiprojekt:<hash> link and syncs
// them to the video waiting for them.
func LinkJob(hash string) func(ctx context.Context, a *App) {
	return func(ctx context.Context, a *App) {
		a.setState(func(s *PanelState) {
			s.Phase, s.Title, s.Detail, s.Busy, s.Total = "running", "Pobieram napisy z NapiProjekt…", "napiprojekt:"+hash, true, 1
		})
		r := batch.NewRunner(a.cfg)
		sub, err := app.DownloadNapiHash(ctx, r, hash)
		if errors.Is(err, napi.ErrNotFound) {
			a.finishError("NapiProjekt nie ma już napisów pod tym linkiem.")
			return
		}
		if err != nil {
			a.finishError("Nie udało się pobrać napisów z linku: " + err.Error())
			return
		}
		movie, _ := r.Movies.MovieInfo(ctx, hash)
		video := ""
		matches := pending.Default().Match(movie)
		if len(matches) == 1 {
			video = matches[0].Video
		} else {
			dir, what := "", "tych napisów"
			if len(matches) > 0 {
				dir = filepath.Dir(matches[0].Video)
			}
			if movie != nil {
				what = movie.Name()
			}
			a.setState(func(s *PanelState) { s.Title, s.Detail = "Wybierz plik filmu…", what })
			if video = a.chooseVideo("Do którego pliku dopasować napisy: "+what+"?", dir); video == "" {
				a.Close()
				return
			}
		}
		a.setState(func(s *PanelState) {
			s.Title, s.Detail = stepText[batch.StepSync]+"…", filepath.Base(video)
		})
		start := time.Now()
		res := r.SyncSubtitles(ctx, video, sub.Content, sub.Lang, batch.FromNapiOther, "napiprojekt:"+hash)
		app.Remember([]batch.Result{res})
		if res.Err != nil {
			if errors.Is(res.Err, context.Canceled) {
				a.finish(nil, "cancelled", "Przerwano dopasowywanie.", "")
				return
			}
			a.finishError("Nie udało się dopasować napisów do " + filepath.Base(video) + ": " + res.Err.Error())
			return
		}
		a.finish([]batch.Result{res}, "ok", fmt.Sprintf("Napisy z innej wersji dopasowane w %ds – sprawdź, czy pasują.",
			int(time.Since(start).Seconds())), res.Output)
	}
}

// resultText is the one-line Polish explanation under a file in the list.
func resultText(r batch.Result) string {
	switch r.Status {
	case batch.Downloaded:
		switch r.Source {
		case batch.FromOS:
			return "Pobrano z OpenSubtitles (" + r.Lang + ")"
		case batch.FromOSSynced:
			return "Z innej wersji (" + r.Release + "), zsynchronizowane"
		case batch.FromNapiOther:
			return "Z innej wersji z NapiProjekt, zsynchronizowane"
		}
		return "Pobrano z NapiProjekt (" + r.Lang + ")"
	case batch.Skipped:
		return "Napisy już były: " + filepath.Base(r.Output)
	case batch.NotFound:
		if r.Warning != "" {
			return "Nie znaleziono (" + r.Warning + ")"
		}
		return "Nie znaleziono napisów"
	}
	if errors.Is(r.Err, context.Canceled) {
		return "Przerwano"
	}
	return app.Message(r)
}
