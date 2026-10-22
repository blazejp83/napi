//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blazejp83/napi/internal/config"
	"github.com/blazejp83/napi/internal/osdb"
	"github.com/blazejp83/napi/internal/winui"
)

// Settings is what the settings window edits.
type Settings struct {
	Config        config.Config `json:"config"`
	Path          string        `json:"path"`
	MenuInstalled bool          `json:"menuInstalled"`
	Portable      bool          `json:"portable"`
}

func (a *App) GetSettings() (Settings, error) {
	cfg, p, err := config.Load()
	s := Settings{Config: cfg, Path: p, MenuInstalled: winui.MenuInstalled()}
	if exe, e := os.Executable(); e == nil {
		s.Portable = filepath.Dir(exe) == filepath.Dir(p)
	}
	return s, err
}

// SaveSettings validates and writes napi.json.
func (a *App) SaveSettings(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	_, p, _ := config.Load()
	if err := cfg.Save(p); err != nil {
		return err
	}
	a.cfg = cfg
	return nil
}

// SetMenu installs or removes the Explorer menu and napiprojekt: links.
func (a *App) SetMenu(enabled bool) error {
	if !enabled {
		return winui.UninstallMenu()
	}
	exe, err := winui.MenuExecutable()
	if err != nil {
		return err
	}
	return winui.InstallMenu(exe, a.cfg.Extensions)
}

// TestOpenSubtitles logs in with the given credentials (not yet saved) and
// reports the remaining daily downloads.
func (a *App) TestOpenSubtitles(o config.OpenSubtitles) (string, error) {
	if o.APIKey == "" {
		return "", errors.New("podaj klucz API")
	}
	if o.UserAgent == "" {
		o.UserAgent = osdb.DefaultUserAgent
	}
	c := osdb.NewClient(o.APIKey, o.Username, o.Password, o.UserAgent)
	c.TokenFile = "" // always log in for real when testing
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if o.Username == "" || o.Password == "" {
		if _, err := c.Search(ctx, osdb.Query{IMDbID: 133093, Languages: []string{"pl"}}); err != nil {
			return "", err
		}
		return "Klucz API działa. Do pobierania podaj też login i hasło.", nil
	}
	info, err := c.UserInfo(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Zalogowano (%s). Pozostało pobrań dzisiaj: %d z %d.",
		info.Level, info.RemainingDownloads, info.AllowedDownloads), nil
}

// ConfigFolder opens the folder holding napi.json.
func (a *App) ConfigFolder() error {
	_, p, _ := config.Load()
	if _, err := os.Stat(p); err != nil {
		return showInExplorer(filepath.Dir(p))
	}
	return showInExplorer(p)
}
