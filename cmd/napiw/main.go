//go:build windows

// Command napiw is napi's Windows interface: run from the Explorer menu (or
// with files dropped on it) it shows progress in a card in the screen
// corner; opened by a napiprojekt: link on napiprojekt.pl it syncs that
// version to the waiting video; started on its own it shows the settings.
package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/blazejp83/napi/internal/app"
	"github.com/blazejp83/napi/internal/config"
	"github.com/blazejp83/napi/internal/gui"
	"github.com/blazejp83/napi/internal/winui"
)

const queueFlag = "--queue"

func main() {
	cfg, _, cfgErr := config.Load()
	args := os.Args[1:]
	// After an update, bring the Explorer entries up to date.
	if exe, err := os.Executable(); err == nil {
		if err := winui.RefreshMenu(exe, cfg.Extensions); err != nil {
			app.LogError("menu refresh", err)
		}
	}
	lang, mode := "", ""
	for len(args) > 0 && strings.HasPrefix(args[0], "--") && args[0] != queueFlag {
		switch {
		case strings.HasPrefix(args[0], "--lang="):
			lang = strings.ToUpper(strings.TrimPrefix(args[0], "--lang="))
		case args[0] == "--sync":
			mode = "sync"
		}
		args = args[1:]
	}
	if len(args) == 0 && lang == "" && mode == "" {
		fail(gui.RunSettings(cfg))
		return
	}
	if len(args) == 0 {
		return
	}
	if cfgErr != nil {
		winui.MessageBox("Napi", "Błąd w pliku ustawień:\n"+cfgErr.Error()+
			"\n\nUruchom napiw.exe bez argumentów, aby otworzyć ustawienia.", winui.MBIconError)
		os.Exit(1)
	}
	if len(args) == 1 {
		if hash, ok := app.NapiLinkHash(args[0]); ok {
			mode, args = "link", []string{hash}
		}
	}

	// Every launch (Explorer starts one per selected item, a browser one per
	// napiprojekt: link) queues its work; the process holding the lock shows
	// one card and runs it all as a job queue, picking up launches that arrive
	// while it is open. The others exit.
	runtime.LockOSThread()
	q := winui.NewQueue()
	if args[0] != queueFlag {
		if err := q.Add(lang, mode, args); err != nil {
			fail(gui.RunPanel(cfg, []winui.Group{{Lang: lang, Mode: mode, Paths: args}}, nil))
			return
		}
	}
	lock, err := winui.NewLock(`Local\napi-menu-queue`)
	if err != nil || !lock.TryLock() {
		return
	}
	groups := q.Drain(400*time.Millisecond, 3*time.Second)
	if len(groups) > 0 {
		// A selection arrives as a burst of launches: wait for it to settle
		// so it becomes one job.
		poll := func() []winui.Group {
			if !q.Pending() {
				return nil
			}
			return q.Drain(400*time.Millisecond, 3*time.Second)
		}
		fail(gui.RunPanel(cfg, groups, poll))
	}
	lock.Unlock()
	// A launch that queued after the last drain saw the lock held and exited;
	// hand its paths to a fresh process (a window can only run once per process).
	if q.Pending() {
		if exe, err := os.Executable(); err == nil {
			exec.Command(exe, queueFlag).Start()
		}
	}
}

func fail(err error) {
	if err != nil {
		winui.MessageBox("Napi", "Nie udało się otworzyć okna:\n"+err.Error()+
			"\n\nnapi potrzebuje środowiska Microsoft Edge WebView2.", winui.MBIconError)
		os.Exit(1)
	}
}
