//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/blazejp83/napi/internal/app"
	"github.com/blazejp83/napi/internal/batch"
	"github.com/blazejp83/napi/internal/config"
	"github.com/blazejp83/napi/internal/napi"
	"github.com/blazejp83/napi/internal/pending"
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
	Jobs    []Job    `json:"jobs"`    // the queue, in order
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
	Page   string `json:"page"` // title to search for on napiprojekt.pl
}

// PageView is a recognised film whose versions can be searched for on
// napiprojekt.pl.
type PageView struct {
	Name  string `json:"name"`  // with year and episode, for the user
	Title string `json:"title"` // what to search for
}

var stepText = map[batch.Step]string{
	batch.StepHash:     "Czytam plik",
	batch.StepNapi:     "Szukam w NapiProjekt",
	batch.StepOS:       "Szukam w OpenSubtitles",
	batch.StepDownload: "Pobieram napisy",
	batch.StepSync:     "Synchronizuję z dźwiękiem filmu",
}

var langWords = map[string]string{"PL": "polskie", "ENG": "angielskie"}

// getJob downloads subtitles for videos and folders from the Explorer menu.
func (a *App) getJob(ctx context.Context, j *Job) jobResult {
	cfg := a.cfg
	if j.Lang != "" {
		cfg.Languages = []string{j.Lang}
	}
	videos, err := batch.Collect(j.paths, cfg)
	if err != nil {
		return jobResult{kind: "error", msg: err.Error()}
	}
	if len(videos) == 0 {
		return jobResult{kind: "none", msg: "Nie znaleziono plików wideo."}
	}
	if !cfg.Overwrite {
		switch a.askReplace(ctx, videos, cfg.Languages[0]) {
		case "replace":
			cfg.Overwrite = true
		case "cancel":
			return jobResult{kind: "kept", msg: "Zostawiono istniejące napisy."}
		}
	}
	return outcome(a.runRound(ctx, cfg, videos, 0))
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

// syncRound re-syncs subtitle files chosen with "Synchronizuj napisy".
func (a *App) syncRound(ctx context.Context, files []string, base int) []batch.Result {
	a.setState(func(s *PanelState) {
		s.Phase, s.Total, s.Done, s.Choices = "running", base+len(files), base, nil
		s.Title, s.Detail = stepText[batch.StepSync]+"…", filepath.Base(files[0])
		s.Busy = base+len(files) == 1
	})
	r := batch.NewRunner(a.cfg)
	r.OnStep = func(video string, step batch.Step) {
		a.setState(func(s *PanelState) { s.Title, s.Detail = stepText[step]+"…", filepath.Base(video) })
	}
	r.Progress = func(done, total int, res batch.Result) {
		a.setState(func(s *PanelState) { s.Done = base + done })
	}
	return r.SyncFiles(ctx, files)
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

// jobResult is how a job ended.
type jobResult struct {
	results []batch.Result
	kind    string // ok, partial, none, error, cancelled, kept (nothing done)
	msg     string
	output  string // the one file produced, for "Pokaż plik"
}

// outcome sums up a job's results.
func outcome(results []batch.Result) jobResult {
	s := batch.Summarize(results)
	cancelled := 0
	for _, r := range results {
		if errors.Is(r.Err, context.Canceled) {
			cancelled++
		}
	}
	kind, msg := "ok", ""
	switch {
	case cancelled > 0:
		kind, msg = "cancelled", fmt.Sprintf("Przerwano. Gotowe %d z %d.", s.Downloaded, len(results))
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
		resyncOnly := true
		for _, r := range results {
			if r.Source != batch.FromResync {
				resyncOnly = false
			}
		}
		msg = summaryText(s, resyncOnly)
	}
	output := ""
	if len(results) == 1 && results[0].Output != "" {
		output = results[0].Output
	}
	return jobResult{results: results, kind: kind, msg: msg, output: output}
}

func singleMessage(r batch.Result) (string, string) {
	name := filepath.Base(r.Video)
	switch r.Status {
	case batch.Downloaded:
		switch r.Source {
		case batch.FromResync:
			return "ok", "Napisy " + filepath.Base(r.Output) + " " + syncedHow(r) + ". Poprzednie są w kopii .bak."
		case batch.FromOSSynced, batch.FromNapiOther:
			if r.Machine {
				return "ok", "Jedyne dostępne napisy: tłumaczenie maszynowe z innej wersji, " + syncedHow(r) + "."
			}
			return "ok", "Napisy z innej wersji, " + syncedHow(r) + "."
		case batch.FromOS:
			return "ok", "Pobrano napisy z OpenSubtitles (" + r.Lang + ")."
		}
		return "ok", "Pobrano napisy z NapiProjekt (" + r.Lang + ")."
	case batch.Skipped:
		return "ok", "Napisy już są: " + filepath.Base(r.Output)
	case batch.NotFound:
		if r.Movie != nil {
			return "none", "Brak napisów do tej wersji. NapiProjekt zna film " + r.Movie.Name() +
				" – wyszukam go na napiprojekt.pl; kliknij tam wybrane napisy, a dopasuję je automatycznie."
		}
		return "none", "Nie znaleziono napisów dla " + name + "."
	}
	return "error", app.Message(r)
}

// summaryText sums up a run; resyncOnly marks a "Synchronizuj napisy" run.
func summaryText(s batch.Summary, resyncOnly bool) string {
	if s.Downloaded > 0 && s.Synced == s.Downloaded && s.Skipped == 0 && s.NotFound == 0 && resyncOnly {
		t := fmt.Sprintf("Zsynchronizowano %d", s.Synced)
		if s.Failed > 0 {
			t += fmt.Sprintf(", błędy %d", s.Failed)
		}
		return t + ". Poprzednie wersje są w kopiach .bak."
	}
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
		if r.Status == batch.Downloaded && (r.Source == batch.FromOSSynced || r.Source == batch.FromNapiOther || r.Source == batch.FromResync) {
			v.Status = "synced"
		}
		if r.Status == batch.NotFound && r.Movie != nil {
			v.Page = r.Movie.Title
			v.Text = "NapiProjekt zna film: " + r.Movie.Name()
			if !seen[r.Movie.ID] {
				seen[r.Movie.ID] = true
				pages = append(pages, PageView{Name: r.Movie.Name(), Title: r.Movie.Title})
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

// linkJob downloads the subtitles behind a napiprojekt:<hash> link and syncs
// them to the video waiting for them (or one the user picks).
func (a *App) linkJob(ctx context.Context, j *Job) jobResult {
	hash := j.paths[0]
	a.setState(func(s *PanelState) {
		s.Phase, s.Title, s.Detail, s.Busy, s.Total, s.Done, s.Choices = "running", "Pobieram napisy z NapiProjekt…", "napiprojekt:"+hash, true, 1, 0, nil
	})
	r := batch.NewRunner(a.cfg)
	sub, err := app.DownloadNapiHash(ctx, r, hash)
	if errors.Is(err, napi.ErrNotFound) {
		return jobResult{kind: "error", msg: "NapiProjekt nie ma już napisów pod tym linkiem."}
	}
	if err != nil {
		return jobResult{kind: "error", msg: "Nie udało się pobrać napisów z linku: " + err.Error()}
	}
	movie, _ := r.Movies.MovieInfo(ctx, hash)
	if movie != nil {
		a.relabel(j, movie.Name())
	}
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
			return jobResult{kind: "kept", msg: "Nie wybrano pliku."}
		}
	}
	a.setState(func(s *PanelState) {
		s.Title, s.Detail = stepText[batch.StepSync]+"…", filepath.Base(video)
	})
	start := time.Now()
	res := r.SyncSubtitles(ctx, video, sub.Content, sub.Lang, batch.FromNapiOther, "napiprojekt:"+hash)
	if res.Err != nil {
		if errors.Is(res.Err, context.Canceled) {
			return jobResult{kind: "cancelled", msg: "Przerwano dopasowywanie."}
		}
		return jobResult{results: []batch.Result{res}, kind: "error",
			msg: "Nie udało się dopasować napisów do " + filepath.Base(video) + ": " + res.Err.Error()}
	}
	return jobResult{results: []batch.Result{res}, kind: "ok", output: res.Output,
		msg: fmt.Sprintf("Napisy z innej wersji %s (%ds).", syncedHow(res), int(time.Since(start).Seconds()))}
}

// syncedHow says what synced subtitles were aligned to: other subtitles of
// the video (reliable) or its sound (worth a check).
func syncedHow(r batch.Result) string {
	if r.SyncAudio || r.SyncRef == "" {
		return "zsynchronizowane z dźwiękiem filmu – sprawdź, czy pasują"
	}
	return "zsynchronizowane według: " + r.SyncRef
}

// resultText is the one-line Polish explanation under a file in the list.
func resultText(r batch.Result) string {
	switch r.Status {
	case batch.Downloaded:
		switch r.Source {
		case batch.FromOS:
			return "Pobrano z OpenSubtitles (" + r.Lang + ")"
		case batch.FromOSSynced:
			if r.Machine {
				return "Tłumaczenie maszynowe z innej wersji (" + r.Release + "), " + syncedHow(r)
			}
			return "Z innej wersji (" + r.Release + "), " + syncedHow(r)
		case batch.FromNapiOther:
			return "Z innej wersji z NapiProjekt, " + syncedHow(r)
		case batch.FromResync:
			return filepath.Base(r.Output) + ": " + syncedHow(r)
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
