//go:build windows

// Package gui is napiw's interface: a progress card in the screen corner for
// Explorer-menu runs and napiprojekt: links, and a settings window. It uses
// Wails (WebView2) with an embedded HTML/CSS/JS frontend.
package gui

import (
	"context"
	"embed"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/blazejp83/napi/internal/app"
	"github.com/blazejp83/napi/internal/config"
)

//go:embed web
var assets embed.FS

const (
	panelTitle    = "Napi – postęp"
	settingsTitle = "Napi – ustawienia"
	panelWidth    = 380
	panelHeight   = 156
)

// App is bound to the frontend; its exported methods are callable from JS
// as window.go.gui.App.<Method>.
type App struct {
	ctx  context.Context
	mode string // "panel" or "settings"
	cfg  config.Config

	job     func(ctx context.Context, a *App) // panel work, run once the page is ready
	cancel  context.CancelFunc
	mu      sync.Mutex
	state   PanelState
	started bool
	answer  chan string // pending question on the card
}

// RunPanel shows the corner card and runs job, which reports through
// a.setState. It returns when the card is closed.
func RunPanel(cfg config.Config, job func(ctx context.Context, a *App)) error {
	a := &App{mode: "panel", cfg: cfg, job: job}
	return wails.Run(&options.App{
		Title:            panelTitle,
		Width:            panelWidth,
		Height:           panelHeight,
		Frameless:        true,
		AlwaysOnTop:      true,
		DisableResize:    true,
		StartHidden:      true,
		BackgroundColour: &options.RGBA{A: 0},
		AssetServer:      &assetserver.Options{Assets: assets},
		Bind:             []any{a},
		OnStartup:        func(ctx context.Context) { a.ctx = ctx },
		OnDomReady:       a.panelReady,
		Windows: &windows.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			BackdropType:         windows.Acrylic,
			Theme:                windows.SystemDefault,
			DisablePinchZoom:     true,
		},
	})
}

// RunSettings shows the settings window.
func RunSettings(cfg config.Config) error {
	a := &App{mode: "settings", cfg: cfg}
	return wails.Run(&options.App{
		Title:            settingsTitle,
		Width:            640,
		Height:           760,
		MinWidth:         520,
		MinHeight:        560,
		BackgroundColour: &options.RGBA{A: 0},
		AssetServer:      &assetserver.Options{Assets: assets},
		Bind:             []any{a},
		OnStartup:        func(ctx context.Context) { a.ctx = ctx },
		Windows: &windows.Options{
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			BackdropType:         windows.Mica,
			Theme:                windows.SystemDefault,
			DisablePinchZoom:     true,
		},
	})
}

func (a *App) panelReady(ctx context.Context) {
	if hwnd := findWindow(panelTitle); hwnd != 0 {
		styleCorner(hwnd)
		placeCorner(hwnd)
	}
	runtime.WindowShow(ctx)
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return
	}
	a.started = true
	jobCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.mu.Unlock()
	go a.job(jobCtx, a)
}

// Mode tells the frontend which view to show.
func (a *App) Mode() string { return a.mode }

// Version is the app version for the settings footer.
func (a *App) Version() string { return app.Version }

// State returns the current panel state (the frontend also receives it
// through "state" events).
func (a *App) State() PanelState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *App) setState(f func(s *PanelState)) {
	a.mu.Lock()
	f(&a.state)
	s := a.state
	a.mu.Unlock()
	runtime.EventsEmit(a.ctx, "state", s)
}

// Cancel stops the running job; the card then shows what was done.
func (a *App) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

// Close quits the app.
func (a *App) Close() {
	a.Cancel()
	runtime.Quit(a.ctx)
}

// Resize changes the card's height (CSS pixels) keeping it in the corner.
func (a *App) Resize(height int) {
	height = max(panelHeight, min(height, 560))
	runtime.WindowSetSize(a.ctx, panelWidth, height)
	if hwnd := findWindow(panelTitle); hwnd != 0 {
		placeCorner(hwnd)
	}
}

// OpenURL opens a web page in the default browser.
func (a *App) OpenURL(u string) { runtime.BrowserOpenURL(a.ctx, u) }

// SearchNapi opens napiprojekt.pl's search for title in the default browser.
// The search is a POST form, so a small local page submits it; the browser
// then passes the site's bot check as for any visit.
func (a *App) SearchNapi(title string) error {
	return openSearchPage(title)
}

// ShowFile opens Explorer with the file selected.
func (a *App) ShowFile(path string) error { return showInExplorer(path) }

// OpenSettings starts napiw in settings mode.
func (a *App) OpenSettings() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return exec.Command(exe).Start()
}

// chooseVideo asks the user which video subtitles from a link belong to.
func (a *App) chooseVideo(title, dir string) string {
	var patterns string
	for i, e := range a.cfg.Extensions {
		if i > 0 {
			patterns += ";"
		}
		patterns += "*." + e
	}
	p, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            title,
		DefaultDirectory: dir,
		Filters:          []runtime.FileFilter{{DisplayName: "Filmy", Pattern: patterns}, {DisplayName: "Wszystkie pliki", Pattern: "*.*"}},
	})
	if err != nil {
		return ""
	}
	return p
}

// sleepCtx waits d unless ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}
