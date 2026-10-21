//go:build windows

// Command napiw is the windowless front end used by the Explorer menu, by
// dropping files onto the exe and by napiprojekt: links on napiprojekt.pl.
// It shows a summary dialog when done; run without arguments it offers to
// install or remove the menu.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/blaze/napi/internal/app"
	"github.com/blaze/napi/internal/batch"
	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/pending"
	"github.com/blaze/napi/internal/winui"
)

const title = "NapiProjekt"

func main() {
	if len(os.Args) < 2 {
		setup()
		return
	}
	cfg, _, err := config.Load()
	if err != nil {
		winui.MessageBox(title, "Błąd w pliku ustawień:\n"+err.Error(), winui.MBIconError)
		os.Exit(1)
	}
	if len(os.Args) == 2 {
		if hash, ok := app.NapiLinkHash(os.Args[1]); ok {
			link(cfg, hash)
			return
		}
	}

	// Explorer starts one process per selected item. Each queues its paths;
	// the process that gets the lock handles all of them, the rest exit.
	runtime.LockOSThread()
	q := winui.NewQueue()
	if err := q.Add(os.Args[1:]); err != nil {
		run(cfg, os.Args[1:]) // spool folder unusable: handle our own paths
		return
	}
	lock, err := winui.NewLock(`Local\napi-menu-queue`)
	if err != nil {
		run(cfg, q.Drain(0, 0))
		return
	}
	for lock.TryLock() {
		for {
			paths := q.Drain(400*time.Millisecond, 3*time.Second)
			if len(paths) == 0 {
				break
			}
			run(cfg, paths)
		}
		lock.Unlock()
		// A launch that queued after our last drain but saw the lock held has
		// exited; pick its paths up.
		if !q.Pending() {
			return
		}
	}
}

func run(cfg config.Config, paths []string) {
	videos, err := batch.Collect(paths, cfg)
	if err != nil {
		winui.MessageBox(title, err.Error(), winui.MBIconError)
		return
	}
	if len(videos) == 0 {
		winui.MessageBox(title, "Nie znaleziono plików wideo.", winui.MBIconWarning)
		return
	}
	results := batch.NewRunner(cfg).Run(context.Background(), videos)
	app.Remember(results)
	logPath := writeLog(results)
	s := batch.Summarize(results)

	icon := uint32(winui.MBIconInfo)
	if s.Failed > 0 || s.NotFound > 0 {
		icon = winui.MBIconWarning
	}
	var b strings.Builder
	if len(results) == 1 {
		r := results[0]
		name := filepath.Base(r.Video)
		switch r.Status {
		case batch.Downloaded:
			fmt.Fprintf(&b, "Pobrano napisy (%s):\n%s", r.Lang, filepath.Base(r.Output))
			switch r.Source {
			case batch.FromOS:
				b.WriteString("\n\nŹródło: OpenSubtitles")
			case batch.FromOSSynced:
				fmt.Fprintf(&b, "\n\nŹródło: OpenSubtitles, napisy do innej wersji (%s)\n"+
					"zsynchronizowane z dźwiękiem filmu – sprawdź, czy pasują.", r.Release)
			}
			if r.Warning != "" {
				fmt.Fprintf(&b, "\n\nUwaga: %s", r.Warning)
			}
		case batch.Skipped:
			fmt.Fprintf(&b, "Napisy już istnieją:\n%s", filepath.Base(r.Output))
		case batch.NotFound:
			fmt.Fprintf(&b, "Nie znaleziono napisów dla:\n%s", name)
		default:
			fmt.Fprintf(&b, "Błąd dla %s:\n%s", name, app.Message(r))
			icon = winui.MBIconError
		}
	} else {
		fmt.Fprintf(&b, "Pobrano: %d\nJuż były: %d\nNie znaleziono: %d\nBłędy: %d",
			s.Downloaded, s.Skipped, s.NotFound, s.Failed)
		if s.Synced > 0 {
			fmt.Fprintf(&b, "\n\nZsynchronizowane z innej wersji filmu: %d (sprawdź, czy pasują)", s.Synced)
		}
		listed := 0
		for _, r := range results {
			if r.Status != batch.NotFound && r.Status != batch.Failed {
				continue
			}
			if listed == 0 {
				b.WriteString("\n\nBez napisów:")
			}
			if listed == 10 {
				fmt.Fprintf(&b, "\n… i %d więcej", s.NotFound+s.Failed-listed)
				break
			}
			b.WriteString("\n• " + filepath.Base(r.Video))
			listed++
		}
	}
	if logPath != "" && len(results) > 1 {
		b.WriteString("\n\nSzczegóły: " + logPath)
	}
	// Videos NapiProjekt recognises but nothing matched: its website usually
	// has subtitles for other versions of the movie to pick by hand.
	var pages []string
	var names []string
	seen := map[string]bool{}
	for _, r := range results {
		if r.Status == batch.NotFound && r.Movie != nil && !seen[r.Movie.ID] {
			seen[r.Movie.ID] = true
			pages = append(pages, r.Movie.PageURL())
			names = append(names, r.Movie.Name())
		}
	}
	if len(pages) == 0 {
		winui.MessageBox(title, b.String(), icon)
		return
	}
	if len(pages) == 1 {
		fmt.Fprintf(&b, "\n\nNapiProjekt rozpoznaje film: %s.\nNa stronie filmu mogą być napisy do innych wersji – "+
			"kliknij tam napisy, a zostaną automatycznie dopasowane do tego pliku.\n\nOtworzyć stronę filmu?", names[0])
	} else {
		fmt.Fprintf(&b, "\n\nNapiProjekt rozpoznaje %d z tych filmów – na ich stronach mogą być napisy do innych wersji:", len(pages))
		for i, n := range names {
			if i == 8 {
				fmt.Fprintf(&b, "\n… i %d więcej", len(names)-i)
				break
			}
			b.WriteString("\n• " + n)
		}
		b.WriteString("\n\nKliknięte tam napisy zostaną automatycznie dopasowane do plików.\nOtworzyć strony filmów?")
	}
	if winui.MessageBox(title, b.String(), winui.MBYesNo|icon) == winui.IDYes {
		for i, p := range pages {
			if i == 8 {
				break
			}
			winui.OpenURL(p)
		}
	}
}

// writeLog saves the last run as CSV in %LOCALAPPDATA%\napi.
func writeLog(results []batch.Result) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "napi", "ostatnie.csv")
	if app.WriteCSV(p, results) != nil {
		return ""
	}
	return p
}

func setup() {
	cfg, _, _ := config.Load()
	switch winui.MessageBox(title,
		"Dodać \"Pobierz napisy\" do menu kontekstowego Eksploratora\n(pliki wideo i foldery)?\n\n"+
			"Tak – dodaj   Nie – usuń   Anuluj – nic\n\n"+
			"Możesz też przeciągnąć filmy lub foldery na ten plik.", winui.MBYesNoCancel|winui.MBIconInfo) {
	case winui.IDYes:
		exe, err := os.Executable()
		if err == nil {
			err = winui.InstallMenu(exe, cfg.Extensions)
		}
		if err != nil {
			winui.MessageBox(title, err.Error(), winui.MBIconError)
			return
		}
		winui.MessageBox(title, "Gotowe. Kliknij prawym przyciskiem film lub folder → \"Pobierz napisy\".\n(Windows 11: \"Pokaż więcej opcji\")", winui.MBIconInfo)
	case winui.IDNo:
		if err := winui.UninstallMenu(); err != nil {
			winui.MessageBox(title, err.Error(), winui.MBIconError)
			return
		}
		winui.MessageBox(title, "Usunięto z menu.", winui.MBIconInfo)
	}
}

// link handles a napiprojekt:<hash> link clicked on a movie's page: it
// downloads that version's subtitles and syncs them to the video waiting for
// them (or one the user picks).
func link(cfg config.Config, hash string) {
	ctx := context.Background()
	r := batch.NewRunner(cfg)
	sub, err := app.DownloadNapiHash(ctx, r, hash)
	if err != nil {
		winui.MessageBox(title, "Nie udało się pobrać napisów z linku:\n"+err.Error(), winui.MBIconError)
		return
	}
	movie, _ := r.Movies.MovieInfo(ctx, hash)
	store := pending.Default()
	video := ""
	switch m := store.Match(movie); len(m) {
	case 1:
		video = m[0].Video
	default:
		dir := ""
		if len(m) > 0 {
			dir = filepath.Dir(m[0].Video)
		}
		what := "tych napisów"
		if movie != nil {
			what = movie.Name()
		}
		video = winui.ChooseVideo("Wybierz plik filmu dla: "+what, dir, cfg.Extensions)
	}
	if video == "" {
		return
	}
	// Syncing reads the whole audio track; tell the user it is under way.
	done := make(chan batch.Result, 1)
	go func() {
		res := r.SyncSubtitles(ctx, video, sub.Content, sub.Lang, batch.FromNapiOther, "napiprojekt:"+hash)
		app.Remember([]batch.Result{res}) // before any dialog, which may be left open
		done <- res
	}()
	var res batch.Result
	select {
	case res = <-done:
	case <-time.After(1500 * time.Millisecond):
		winui.MessageBox(title, "Pobrano napisy ("+sub.Lang+"). Trwa dopasowywanie do:\n"+filepath.Base(video)+
			"\n\nTo potrwa około minuty – wynik pokaże się po zakończeniu.", winui.MBIconInfo)
		res = <-done
	}
	if res.Err != nil {
		winui.MessageBox(title, "Nie udało się dopasować napisów do "+filepath.Base(video)+":\n"+res.Err.Error(), winui.MBIconError)
		return
	}
	winui.MessageBox(title, "Gotowe – napisy dopasowane do filmu:\n"+filepath.Base(res.Output)+
		"\n\nNapisy pochodzą z innej wersji filmu, sprawdź, czy pasują.", winui.MBIconInfo)
}
