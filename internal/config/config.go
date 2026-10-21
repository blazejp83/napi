// Package config loads napi settings from napi.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blaze/napi/internal/napi"
	"github.com/blaze/napi/internal/osdb"
	"github.com/blaze/napi/internal/subs"
)

const FileName = "napi.json"

type Config struct {
	// Languages to try in order, e.g. ["PL", "ENG"].
	Languages []string `json:"languages"`
	// Format is "srt" (convert everything to SubRip) or "original".
	Format string `json:"format"`
	// Encoding of written files: "utf8bom", "utf8" or "cp1250".
	Encoding string `json:"encoding"`
	// LangSuffix names files movie.pl.srt instead of movie.srt.
	LangSuffix bool `json:"langSuffix"`
	// Overwrite replaces existing subtitles instead of skipping the video.
	Overwrite bool `json:"overwrite"`
	// Recursive descends into subfolders of the given folders.
	Recursive bool `json:"recursive"`
	// Jobs is the number of videos processed in parallel.
	Jobs int `json:"jobs"`
	// DefaultFPS is used for MicroDVD subtitles when the video's frame rate
	// cannot be read.
	DefaultFPS float64 `json:"defaultFps"`
	// Client is the client name sent to the API (it must be whitelisted).
	Client string `json:"client"`
	// Extensions of video files picked up from folders.
	Extensions []string `json:"extensions"`

	// OpenSubtitles is used when NapiProjekt has nothing for a file. Searching
	// needs an API key (opensubtitles.com/consumers); downloading also needs
	// the account's username and password.
	OpenSubtitles OpenSubtitles `json:"openSubtitles"`
	// Sync allows subtitles made for another release of the same movie,
	// aligned to the video's audio with alass.
	Sync bool `json:"sync"`
	// AlassPath overrides the alass executable; by default napi uses one on
	// PATH or downloads the pinned release on first use.
	AlassPath string `json:"alassPath"`
}

type OpenSubtitles struct {
	APIKey    string `json:"apiKey"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	UserAgent string `json:"userAgent"`
}

func Default() Config {
	return Config{
		Languages:  []string{"PL"},
		Format:     "srt",
		Encoding:   subs.EncUTF8BOM,
		Recursive:  true,
		Jobs:       4,
		DefaultFPS: 23.976,
		Client:     napi.DefaultClient,
		Sync:       true,
		Extensions: []string{"avi", "mkv", "mp4", "m4v", "mov", "wmv", "mpg", "mpeg", "ts", "m2ts",
			"vob", "divx", "flv", "webm", "ogm", "rmvb", "3gp", "asf"},
	}
}

// Path returns where the config file lives: next to the executable when a
// napi.json already exists there (portable mode), otherwise in the user's
// config directory (%APPDATA%\napi on Windows).
func Path() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), FileName)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return FileName
	}
	return filepath.Join(dir, "napi", FileName)
}

// Load reads the config file, falling back to defaults for a missing file
// and for any field the file leaves out.
func Load() (Config, string, error) {
	c := Default()
	p := Path()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, p, c.Validate()
	}
	if err != nil {
		return c, p, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Default(), p, fmt.Errorf("%s: %w", p, err)
	}
	return c, p, c.Validate()
}

// Save writes the config as indented JSON, creating its directory.
func (c Config) Save(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o644)
}

// Validate normalises values and rejects unsupported ones.
func (c *Config) Validate() error {
	for i, l := range c.Languages {
		l = strings.ToUpper(strings.TrimSpace(l))
		switch l {
		case "EN":
			l = "ENG"
		case "PL", "ENG":
		default:
			return fmt.Errorf("unsupported language %q (use PL or ENG)", l)
		}
		c.Languages[i] = l
	}
	if len(c.Languages) == 0 {
		c.Languages = []string{"PL"}
	}
	c.Format = strings.ToLower(c.Format)
	if c.Format != "srt" && c.Format != "original" {
		return fmt.Errorf("unsupported format %q (use srt or original)", c.Format)
	}
	c.Encoding = strings.ToLower(strings.ReplaceAll(c.Encoding, "-", ""))
	if !subs.ValidEncoding(c.Encoding) {
		return fmt.Errorf("unsupported encoding %q (use utf8bom, utf8 or cp1250)", c.Encoding)
	}
	if c.Jobs < 1 {
		c.Jobs = 1
	}
	if c.Jobs > 16 {
		c.Jobs = 16
	}
	if c.DefaultFPS <= 0 {
		c.DefaultFPS = 23.976
	}
	if c.Client == "" {
		c.Client = napi.DefaultClient
	}
	if c.OpenSubtitles.UserAgent == "" {
		c.OpenSubtitles.UserAgent = osdb.DefaultUserAgent
	}
	return nil
}
