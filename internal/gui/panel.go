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
	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/pending"
	"github.com/blaze/napi/internal/winui"
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

	Choices []Choice `json:"choices"` // phase "ask": buttons to answer with
}

// Choice is one answer button of a question on the card.
type Choice struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Primary bool   `json:"primary"`
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

var langWords = map[string]string{"PL": "polskie", "ENG": "angielskie"}

// BatchJob processes videos and folders from the Explorer menu. Each group
// carries the language its menu entry asked for; more is polled after each
// round for groups queued by later menu launches.
func BatchJob(groups []winui.Group, more func() []winui.Group) func(ctx context.Context, a *App) {
	return func(ctx context.Context, a *App) {
		var all []batch.Result
		for len(groups) > 0 && ctx.Err() == nil {
			g := groups[0]
			groups = groups[1:]
			cfg := a.cfg
			if g.Lang != "" {
				cfg.Languages = []string{g.Lang}
			}
			videos, err := batch.Collect(g.Paths, cfg)
			if err != nil {
				a.finishError(err.Error())
				return
			}
			if !cfg.Overwrite {
				switch a.askReplace(ctx, videos, cfg.Languages[0]) {
				case "replace":
					cfg.Overwrite = true
				case "cancel":
					videos = nil
				}
			}
			if ctx.Err() != nil {
				break
			}
			all = append(all, a.runRound(ctx, cfg, videos, len(all))...)
			if len(groups) == 0 && ctx.Err() == nil {
				groups = more()
			}
		}
		if len(all) == 0 {
			if ctx.Err() == nil {
				a.Close() // nothing to do: the user kept the existing subtitles
			}
			return
		}
		app.Remember(all)
		a.finishBatch(ctx, all)
	}
}

func (a *App) runRound(ctx context.Context, cfg config.Config, videos []string, base int) []batch.Result {
	if len(videos) == 0 {
		return nil
	}
	a.setState(func(s *PanelState) {
		s.Phase, s.Total, s.Done, s.Choices = "running", base+len(videos), base, nil
		s.Title, s.Detail, s.Busy = "Szukam napisów…", "", false
	})
	r := batch.NewRunner(cfg)
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
	return r.Run(ctx, videos)
}

// askReplace asks what to do when some videos already have subtitles in
// lang: "replace" them (old ones are kept as .bak), "skip" those videos, or
// "cancel" the round. It returns "skip" when there is nothing to ask.
func (a *App) askReplace(ctx context.Context, videos []string, lang string) string {
	var have []string
	for _, v := range videos {
		if ex := batch.ExistingFor(v, lang, true); len(ex) > 0 {
			have = append(have, filepath.Base(ex[0]))
		}
	}
	if len(have) == 0 {
		return "skip"
	}
	words := langWords[lang]
	keep := " Obecne zachowam jako kopię .bak."
	switch {
	case len(videos) == 1:
		return a.ask(ctx, "Napisy już są", fmt.Sprintf("%s (%s). Pobrać nowe?%s", have[0], words, keep),
			Choice{"replace", "Pobierz nowe", true}, Choice{"cancel", "Zostaw", false})
	case len(have) == len(videos):
		return a.ask(ctx, "Napisy już są", fmt.Sprintf("Wszystkie %s mają już napisy (%s). Pobrać ponownie?%s", filesPL(len(videos)), words, keep),
			Choice{"replace", "Pobierz ponownie", true}, Choice{"cancel", "Anuluj", false})
	}
	return a.ask(ctx, "Część filmów ma już napisy", fmt.Sprintf("%d z %s ma już napisy (%s). Pobrać tylko brakujące?",
		len(have), filesPL(len(videos)), words),
		Choice{"skip", "Tylko brakujące", true}, Choice{"replace", "Wszystkie od nowa", false})
}

// ask shows a question on the card and waits for one of choices.
func (a *App) ask(ctx context.Context, title, message string, choices ...Choice) string {
	a.mu.Lock()
	a.answer = make(chan string, 1)
	ch := a.answer
	a.mu.Unlock()
	a.setState(func(s *PanelState) {
		s.Phase, s.Title, s.Message, s.Choices, s.Busy = "ask", title, message, choices, false
	})
	select {
	case id := <-ch:
		return id
	case <-ctx.Done():
		return "cancel"
	}
}

// Answer delivers the user's choice for the current question.
func (a *App) Answer(id string) {
	a.mu.Lock()
	ch := a.answer
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- id:
		default:
		}
	}
}

// filesPL formats a Polish count of videos: 1 film, 3 filmy, 5 filmów.
func filesPL(n int) string {
	d, t := n%10, n%100
	switch {
	case n == 1:
		return "1 film"
	case d >= 2 && d <= 4 && (t < 12 || t > 14):
		return fmt.Sprintf("%d filmy", n)
	}
	return fmt.Sprintf("%d filmów", n)
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
		s.Phase, s.Kind, s.Message, s.Busy, s.Choices = "done", kind, msg, false, nil
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
