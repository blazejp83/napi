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
	"time"

	"github.com/blaze/napi/internal/app"
	"github.com/blaze/napi/internal/config"
	"github.com/blaze/napi/internal/gui"
	"github.com/blaze/napi/internal/winui"
)

const queueFlag = "--queue"

func main() {
	cfg, _, cfgErr := config.Load()
	args := os.Args[1:]
	if len(args) == 0 {
		fail(gui.RunSettings(cfg))
		return
	}
	if cfgErr != nil {
		winui.MessageBox("NapiProjekt", "Błąd w pliku ustawień:\n"+cfgErr.Error()+
			"\n\nUruchom napiw.exe bez argumentów, aby otworzyć ustawienia.", winui.MBIconError)
		os.Exit(1)
	}
	if len(args) == 1 {
		if hash, ok := app.NapiLinkHash(args[0]); ok {
			fail(gui.RunPanel(cfg, gui.LinkJob(hash)))
			return
		}
	}

	// Explorer starts one process per selected item. Each queues its paths;
	// the process holding the lock shows one card for all of them, the rest
	// exit. Paths queued while it runs are picked up after its current round.
	runtime.LockOSThread()
	q := winui.NewQueue()
	if args[0] != queueFlag {
		if err := q.Add(args); err != nil {
			fail(gui.RunPanel(cfg, gui.BatchJob(args, func() []string { return nil })))
			return
		}
	}
	lock, err := winui.NewLock(`Local\napi-menu-queue`)
	if err != nil || !lock.TryLock() {
		return
	}
	paths := q.Drain(400*time.Millisecond, 3*time.Second)
	if len(paths) > 0 {
		fail(gui.RunPanel(cfg, gui.BatchJob(paths, func() []string { return q.Drain(0, 0) })))
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
		winui.MessageBox("NapiProjekt", "Nie udało się otworzyć okna:\n"+err.Error()+
			"\n\nnapi potrzebuje środowiska Microsoft Edge WebView2.", winui.MBIconError)
		os.Exit(1)
	}
}
