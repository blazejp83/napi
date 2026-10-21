// Package proc runs helper programs (ffprobe, alass) without console windows.
package proc

import (
	"os/exec"
	"syscall"
)

// HideWindow stops a console window from flashing when the GUI build runs a
// console program.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
