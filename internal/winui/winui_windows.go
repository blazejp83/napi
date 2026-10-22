// Package winui contains the Windows integration: the Explorer context menu,
// message boxes and the queue that merges multi-selection launches.
package winui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	verbKey = `napi`
	classes = `Software\Classes`
	// menuSchema changes whenever the registered entries change, so an
	// updated napiw re-registers itself (see RefreshMenu).
	menuSchema = "3"
	stateKey   = `Software\napi`
)

// verbs are the Explorer menu entries: key name, label, extra arguments.
var verbs = []struct{ key, label, args string }{
	{verbKey, "Pobierz napisy", ""},
	{verbKey + "-eng", "Pobierz napisy (eng)", "--lang=ENG "},
}

// syncVerb is the entry on subtitle files: re-sync them to their video.
var syncVerb = struct{ key, label, args string }{verbKey + "-sync", "Synchronizuj napisy", "--sync "}

// subtitleExts get the sync entry.
var subtitleExts = []string{"srt", "sub"}

// MenuExecutable is the program the menu runs: napiw.exe next to the current
// executable (no console window), or the current executable itself.
func MenuExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	w := filepath.Join(filepath.Dir(exe), "napiw.exe")
	if _, err := os.Stat(w); err == nil {
		return w, nil
	}
	return exe, nil
}

func verbParents(exts []string) []string {
	parents := []string{`Directory`, `Directory\Background`, `Drive`}
	for _, e := range exts {
		parents = append(parents, `SystemFileAssociations\.`+strings.ToLower(strings.TrimPrefix(e, ".")))
	}
	return parents
}

const protocolKey = classes + `\napiprojekt`

// installProtocol makes napiprojekt:<hash> links (on movie pages at
// napiprojekt.pl) open napiw. It is registered for the current user, so it
// takes precedence over the official client's machine-wide handler until
// UninstallMenu removes it.
func installProtocol(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, protocolKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	k.SetStringValue("", "URL:NapiProjekt")
	k.SetStringValue("URL Protocol", "")
	k.Close()
	for sub, val := range map[string]string{
		`\DefaultIcon`:        exe,
		`\shell\open\command`: fmt.Sprintf(`"%s" "%%1"`, exe),
	} {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, protocolKey+sub, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue("", val)
		k.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func uninstallProtocol() error {
	for _, k := range []string{`\shell\open\command`, `\shell\open`, `\shell`, `\DefaultIcon`, ""} {
		if err := registry.DeleteKey(registry.CURRENT_USER, protocolKey+k); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("%s: %w", protocolKey+k, err)
		}
	}
	return nil
}

// InstallMenu registers the context-menu verb and the napiprojekt: link
// handler for the current user.
func InstallMenu(exe string, exts []string) error {
	if err := installProtocol(exe); err != nil {
		return fmt.Errorf("napiprojekt: links: %w", err)
	}
	for _, parent := range verbParents(exts) {
		arg := "%1"
		if parent == `Directory\Background` {
			arg = "%V"
		}
		for _, v := range verbs {
			base := classes + `\` + parent + `\shell\` + v.key
			k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
			if err != nil {
				return fmt.Errorf("%s: %w", base, err)
			}
			k.SetStringValue("", v.label)
			k.SetStringValue("Icon", exe)
			// "Player" lets Explorer pass large selections instead of capping at 15.
			k.SetStringValue("MultiSelectModel", "Player")
			k.Close()
			c, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\command`, registry.SET_VALUE)
			if err != nil {
				return fmt.Errorf("%s: %w", base, err)
			}
			err = c.SetStringValue("", fmt.Sprintf(`"%s" %s"%s"`, exe, v.args, arg))
			c.Close()
			if err != nil {
				return err
			}
		}
	}
	for _, e := range subtitleExts {
		base := classes + `\SystemFileAssociations\.` + e + `\shell\` + syncVerb.key
		k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
		k.SetStringValue("", syncVerb.label)
		k.SetStringValue("Icon", exe)
		k.SetStringValue("MultiSelectModel", "Player")
		k.Close()
		c, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\command`, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
		err = c.SetStringValue("", fmt.Sprintf(`"%s" %s"%%1"`, exe, syncVerb.args))
		c.Close()
		if err != nil {
			return err
		}
	}
	if k, _, err := registry.CreateKey(registry.CURRENT_USER, stateKey, registry.SET_VALUE); err == nil {
		k.SetStringValue("MenuSchema", menuSchema)
		k.SetStringValue("MenuExe", exe)
		k.Close()
	}
	return nil
}

// RefreshMenu re-registers the menu when it is installed but was written by
// an older napiw (different entries) or points at another exe.
func RefreshMenu(exe string, exts []string) error {
	if !MenuInstalled() {
		return nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, stateKey, registry.QUERY_VALUE)
	if err == nil {
		schema, _, _ := k.GetStringValue("MenuSchema")
		at, _, _ := k.GetStringValue("MenuExe")
		k.Close()
		if schema == menuSchema && strings.EqualFold(at, exe) {
			return nil
		}
	}
	UninstallMenu()
	return InstallMenu(exe, exts)
}

// UninstallMenu removes every verb InstallMenu may have created and the
// link handler.
func UninstallMenu() error {
	if err := uninstallProtocol(); err != nil {
		return err
	}
	root, err := registry.OpenKey(registry.CURRENT_USER, classes+`\SystemFileAssociations`, registry.ENUMERATE_SUB_KEYS)
	var parents []string
	if err == nil {
		names, _ := root.ReadSubKeyNames(-1)
		root.Close()
		for _, n := range names {
			parents = append(parents, `SystemFileAssociations\`+n)
		}
	}
	parents = append(parents, verbParents(nil)...)
	all := append(append([]struct{ key, label, args string }{}, verbs...), syncVerb)
	for _, parent := range parents {
		for _, v := range all {
			base := classes + `\` + parent + `\shell\` + v.key
			for _, k := range []string{base + `\command`, base} {
				if err := registry.DeleteKey(registry.CURRENT_USER, k); err != nil && !errors.Is(err, registry.ErrNotExist) {
					return fmt.Errorf("%s: %w", k, err)
				}
			}
		}
	}
	registry.DeleteKey(registry.CURRENT_USER, stateKey)
	return nil
}

// Message box flags.
const (
	MBOK          = 0x0
	MBYesNo       = 0x4
	MBYesNoCancel = 0x3
	MBIconError   = 0x10
	MBIconWarning = 0x30
	MBIconInfo    = 0x40
	IDYes         = 6
	IDNo          = 7
)

var procMessageBox = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")

// MessageBox shows a modal dialog and returns the button pressed.
func MessageBox(title, text string, flags uint32) int {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(flags|0x10000)) // MB_SETFOREGROUND
	return int(r)
}

// Lock is a named mutex that elects one process to handle queued paths.
// Windows mutexes belong to a thread, so callers must runtime.LockOSThread.
type Lock struct{ h windows.Handle }

func NewLock(name string) (*Lock, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, n)
	if h == 0 {
		return nil, err
	}
	return &Lock{h}, nil
}

// TryLock takes the mutex if no other process holds it.
func (l *Lock) TryLock() bool {
	ev, _ := windows.WaitForSingleObject(l.h, 0)
	return ev == windows.WAIT_OBJECT_0 || ev == windows.WAIT_ABANDONED
}

func (l *Lock) Unlock() { windows.ReleaseMutex(l.h) }

// MenuInstalled reports whether the Explorer menu verb is registered.
func MenuInstalled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, classes+`\Directory\shell\`+verbKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}
