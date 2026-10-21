//go:build !windows

// Package proc runs helper programs (ffprobe, alass) without console windows.
package proc

import "os/exec"

func HideWindow(*exec.Cmd) {}
