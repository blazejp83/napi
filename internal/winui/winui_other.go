//go:build !windows

package winui

import "errors"

var errWindowsOnly = errors.New("the Explorer menu is only available on Windows")

func MenuExecutable() (string, error)             { return "", errWindowsOnly }
func InstallMenu(exe string, exts []string) error { return errWindowsOnly }
func UninstallMenu() error                        { return errWindowsOnly }
