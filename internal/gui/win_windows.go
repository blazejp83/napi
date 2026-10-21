package gui

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procSPI          = user32.NewProc("SystemParametersInfoW")
	procFindWindow   = user32.NewProc("FindWindowW")
	procGetLong      = user32.NewProc("GetWindowLongPtrW")
	procSetLong      = user32.NewProc("SetWindowLongPtrW")
	procGetRect      = user32.NewProc("GetWindowRect")
	procSetPos       = user32.NewProc("SetWindowPos")
	procGetDpiWindow = user32.NewProc("GetDpiForWindow")
	procDwmSet       = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

type rect struct{ left, top, right, bottom int32 }

func findWindow(title string) uintptr {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	h, _, _ := procFindWindow.Call(0, uintptr(unsafe.Pointer(t)))
	return h
}

// styleCorner turns the panel window into a tool window (no taskbar button
// or Alt+Tab entry) with Windows 11 rounded corners.
func styleCorner(hwnd uintptr) {
	const gwlStyle, gwlExStyle = ^uintptr(15), ^uintptr(19) // -16, -20
	ex, _, _ := procGetLong.Call(hwnd, gwlExStyle)
	procSetLong.Call(hwnd, gwlExStyle, (ex|0x80)&^0x40000) // +WS_EX_TOOLWINDOW, -WS_EX_APPWINDOW
	// Without the system menu and min/max boxes DWM stops drawing its own
	// caption buttons (a red close button) into the translucent frame.
	st, _, _ := procGetLong.Call(hwnd, gwlStyle)
	procSetLong.Call(hwnd, gwlStyle, st&^(0x80000|0x20000|0x10000)) // WS_SYSMENU, WS_MINIMIZEBOX, WS_MAXIMIZEBOX
	const swpNoSize, swpNoMove, swpNoZOrder, swpNoActivate, swpFrameChanged = 0x1, 0x2, 0x4, 0x10, 0x20
	procSetPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
	round := uint32(2)                                     // DWMWCP_ROUND
	procDwmSet.Call(hwnd, 33, uintptr(unsafe.Pointer(&round)), 4)
}

// placeCorner moves the window to the bottom-right of the work area (above
// the taskbar), in physical pixels so DPI scaling cannot misplace it.
func placeCorner(hwnd uintptr) {
	var work, win rect
	procSPI.Call(0x30, 0, uintptr(unsafe.Pointer(&work)), 0) // SPI_GETWORKAREA
	procGetRect.Call(hwnd, uintptr(unsafe.Pointer(&win)))
	dpi, _, _ := procGetDpiWindow.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	margin := int32(12 * dpi / 96)
	w, h := win.right-win.left, win.bottom-win.top
	x, y := work.right-w-margin, work.bottom-h-margin
	const swpNoSize, swpNoZOrder, swpNoActivate = 0x1, 0x4, 0x10
	procSetPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

// showInExplorer opens Explorer with path selected.
func showInExplorer(path string) error {
	cmd := exec.Command("explorer.exe")
	// Explorer wants the path glued to the switch: /select,"C:\a b\c.srt"
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}
