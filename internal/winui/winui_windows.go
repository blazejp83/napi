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
	verbKey   = `napi`
	menuLabel = "Pobierz napisy (NapiProjekt)"
	classes   = `Software\Classes`
)

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
		base := classes + `\` + parent + `\shell\` + verbKey
		k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
		k.SetStringValue("", menuLabel)
		k.SetStringValue("Icon", exe)
		// "Player" lets Explorer pass large selections instead of capping at 15.
		k.SetStringValue("MultiSelectModel", "Player")
		k.Close()
		c, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\command`, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("%s: %w", base, err)
		}
		err = c.SetStringValue("", fmt.Sprintf(`"%s" "%s"`, exe, arg))
		c.Close()
		if err != nil {
			return err
		}
	}
	return nil
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
	for _, parent := range parents {
		base := classes + `\` + parent + `\shell\` + verbKey
		for _, k := range []string{base + `\command`, base} {
			if err := registry.DeleteKey(registry.CURRENT_USER, k); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
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

// OpenURL opens a web page in the default browser.
func OpenURL(u string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

var procGetOpenFileName = windows.NewLazySystemDLL("comdlg32.dll").NewProc("GetOpenFileNameW")

type openFileName struct {
	structSize    uint32
	owner         uintptr
	instance      uintptr
	filter        *uint16
	customFilter  *uint16
	maxCustFilter uint32
	filterIndex   uint32
	file          *uint16
	maxFile       uint32
	fileTitle     *uint16
	maxFileTitle  uint32
	initialDir    *uint16
	title         *uint16
	flags         uint32
	fileOffset    uint16
	fileExtension uint16
	defExt        *uint16
	custData      uintptr
	hook          uintptr
	templateName  *uint16
	reserved      uintptr
	reservedDW    uint32
	flagsEx       uint32
}

// ChooseVideo shows the standard Open dialog for one video file and returns
// its path, or "" when cancelled.
func ChooseVideo(title, initialDir string, exts []string) string {
	var patterns []string
	for _, e := range exts {
		patterns = append(patterns, "*."+strings.TrimPrefix(e, "."))
	}
	filter := "Filmy\x00" + strings.Join(patterns, ";") + "\x00Wszystkie pliki\x00*.*\x00\x00"
	f16 := utf16Raw(filter)
	buf := make([]uint16, 32768)
	t, _ := windows.UTF16PtrFromString(title)
	ofn := openFileName{
		filter:  &f16[0],
		file:    &buf[0],
		maxFile: uint32(len(buf)),
		title:   t,
		flags:   0x1000 | 0x800 | 0x80000 | 0x8, // FILEMUSTEXIST | PATHMUSTEXIST | EXPLORER | NOCHANGEDIR
	}
	ofn.structSize = uint32(unsafe.Sizeof(ofn))
	if initialDir != "" {
		ofn.initialDir, _ = windows.UTF16PtrFromString(initialDir)
	}
	if r, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// utf16Raw encodes s keeping embedded NULs, as dialog filters need.
func utf16Raw(s string) []uint16 {
	var out []uint16
	for _, r := range s {
		out = append(out, uint16(r))
	}
	return out
}
