package osdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blazejp83/napi/internal/napi"
)

const (
	DefaultBaseURL = "https://api.opensubtitles.com/api/v1"
	// DefaultUserAgent identifies the app, as the API requires ("name vX.Y").
	DefaultUserAgent = "napi v0.2.0"
)

var (
	// ErrQuota means the account's daily download limit is used up.
	ErrQuota = errors.New("OpenSubtitles daily download limit reached")
	// ErrAuth means the API key or login was rejected.
	ErrAuth = errors.New("OpenSubtitles rejected the API key or login")
)

// Subtitle is one search result.
type Subtitle struct {
	ID                string
	Language          string // ISO 639-1, e.g. "pl"
	Release           string
	FPS               float64
	DownloadCount     int
	HearingImpaired   bool
	AITranslated      bool
	MachineTranslated bool
	ForeignPartsOnly  bool
	MovieHashMatch    bool
	FromTrusted       bool
	IMDbID            int
	Title             string
	Year              int
	ParentTitle       string // the series, for episodes
	Files             []File
}

type File struct {
	ID   int
	Name string
}

// Client talks to the REST API. Searching needs only the API key;
// downloading also needs a user login, done lazily and cached on disk.
type Client struct {
	APIKey, Username, Password string
	UserAgent                  string
	BaseURL                    string
	HTTP                       *http.Client
	// TokenFile caches the login token between runs ("" disables caching).
	TokenFile string

	mu    sync.Mutex
	token string
	host  string // per-account API host returned by login
}

func NewClient(apiKey, username, password, userAgent string) *Client {
	c := &Client{
		APIKey: apiKey, Username: username, Password: password, UserAgent: userAgent,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
	if dir, err := os.UserCacheDir(); err == nil {
		c.TokenFile = filepath.Join(dir, "napi", "opensubtitles-token.json")
	}
	return c
}

// Configured reports whether an API key is set.
func (c *Client) Configured() bool { return c != nil && c.APIKey != "" }

// Query selects subtitles; zero fields are omitted.
type Query struct {
	MovieHash string
	IMDbID    int    // movie, or the series when Season/Episode are set
	Text      string // title, used when there is no IMDb ID
	Year      int
	// Season and Episode select one episode of a series.
	Season, Episode int
	Languages       []string // ISO 639-1
}

func (c *Client) do(ctx context.Context, method, endpoint string, body any, auth bool, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Api-Key", c.APIKey)
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &napi.TemporaryError{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &napi.TemporaryError{Err: err}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return &napi.TemporaryError{Err: fmt.Errorf("OpenSubtitles HTTP %d", resp.StatusCode)}
	case resp.StatusCode == http.StatusNotAcceptable:
		return ErrQuota
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d: %s)", ErrAuth, resp.StatusCode, apiMessage(data))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("OpenSubtitles HTTP %d: %s", resp.StatusCode, apiMessage(data))
	}
	return json.Unmarshal(data, out)
}

func apiMessage(data []byte) string {
	var m struct {
		Message string   `json:"message"`
		Errors  []string `json:"errors"`
	}
	if json.Unmarshal(data, &m) == nil {
		if m.Message != "" {
			return m.Message
		}
		if len(m.Errors) > 0 {
			return strings.Join(m.Errors, "; ")
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Search returns matching subtitles, at most one page (the API's 50 best).
func (c *Client) Search(ctx context.Context, q Query) ([]Subtitle, error) {
	v := url.Values{}
	if q.MovieHash != "" {
		v.Set("moviehash", strings.ToLower(q.MovieHash))
	}
	episode := q.Episode > 0
	if q.IMDbID > 0 {
		if episode {
			v.Set("parent_imdb_id", strconv.Itoa(q.IMDbID))
		} else {
			v.Set("imdb_id", strconv.Itoa(q.IMDbID))
		}
	}
	if episode {
		v.Set("season_number", strconv.Itoa(q.Season))
		v.Set("episode_number", strconv.Itoa(q.Episode))
	}
	if q.Text != "" && q.IMDbID == 0 {
		v.Set("query", strings.ToLower(q.Text))
	}
	if q.Year > 0 && q.IMDbID == 0 {
		v.Set("year", strconv.Itoa(q.Year))
	}
	if len(q.Languages) > 0 {
		v.Set("languages", strings.ToLower(strings.Join(q.Languages, ",")))
	}
	// The API redirects unless parameters are sorted, which Encode does.
	var resp searchResponse
	if err := c.do(ctx, http.MethodGet, c.BaseURL+"/subtitles?"+v.Encode(), nil, false, &resp); err != nil {
		return nil, err
	}
	out := make([]Subtitle, 0, len(resp.Data))
	for _, d := range resp.Data {
		a := d.Attributes
		s := Subtitle{
			ID: d.ID, Language: a.Language, Release: a.Release, FPS: float64(a.FPS),
			DownloadCount: int(a.DownloadCount), HearingImpaired: a.HearingImpaired,
			AITranslated: a.AITranslated, MachineTranslated: a.MachineTranslated,
			ForeignPartsOnly: a.ForeignPartsOnly, MovieHashMatch: a.MovieHashMatch,
			FromTrusted: a.FromTrusted, IMDbID: int(a.FeatureDetails.IMDbID), Title: a.FeatureDetails.Title,
			ParentTitle: a.FeatureDetails.ParentTitle, Year: int(a.FeatureDetails.Year),
		}
		for _, f := range a.Files {
			s.Files = append(s.Files, File{ID: int(f.FileID), Name: f.FileName})
		}
		out = append(out, s)
	}
	return out, nil
}

// num accepts JSON numbers, numeric strings and null.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil // tolerate odd values rather than failing the whole search
	}
	*n = num(f)
	return nil
}

type searchResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Language          string `json:"language"`
			Release           string `json:"release"`
			FPS               num    `json:"fps"`
			DownloadCount     num    `json:"download_count"`
			HearingImpaired   bool   `json:"hearing_impaired"`
			AITranslated      bool   `json:"ai_translated"`
			MachineTranslated bool   `json:"machine_translated"`
			ForeignPartsOnly  bool   `json:"foreign_parts_only"`
			MovieHashMatch    bool   `json:"moviehash_match"`
			FromTrusted       bool   `json:"from_trusted"`
			FeatureDetails    struct {
				IMDbID      num    `json:"imdb_id"`
				Title       string `json:"title"`
				Year        num    `json:"year"`
				ParentTitle string `json:"parent_title"`
			} `json:"feature_details"`
			Files []struct {
				FileID   num    `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

type tokenCache struct {
	Token    string    `json:"token"`
	Host     string    `json:"host"`
	User     string    `json:"user"`
	Obtained time.Time `json:"obtained"`
}

// login obtains a token, reusing the cached one for up to 20 hours (tokens
// are valid for 24).
func (c *Client) login(ctx context.Context) error {
	if c.token != "" {
		return nil
	}
	if c.Username == "" || c.Password == "" {
		return fmt.Errorf("%w: username and password are needed to download (set openSubtitles in napi.json)", ErrAuth)
	}
	if c.TokenFile != "" {
		var tc tokenCache
		if b, err := os.ReadFile(c.TokenFile); err == nil && json.Unmarshal(b, &tc) == nil &&
			tc.User == c.Username && time.Since(tc.Obtained) < 20*time.Hour && tc.Token != "" {
			c.token, c.host = tc.Token, tc.Host
			return nil
		}
	}
	var resp struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	err := c.do(ctx, http.MethodPost, c.BaseURL+"/login",
		map[string]string{"username": c.Username, "password": c.Password}, false, &resp)
	if err != nil {
		return err
	}
	if resp.Token == "" {
		return fmt.Errorf("%w: login returned no token", ErrAuth)
	}
	c.token, c.host = resp.Token, resp.BaseURL
	if c.TokenFile != "" {
		b, _ := json.Marshal(tokenCache{Token: c.token, Host: c.host, User: c.Username, Obtained: time.Now()})
		if os.MkdirAll(filepath.Dir(c.TokenFile), 0o700) == nil {
			os.WriteFile(c.TokenFile, b, 0o600)
		}
	}
	return nil
}

// apiBase is the per-account host from login when it differs from BaseURL's.
func (c *Client) apiBase() string {
	if c.host == "" {
		return c.BaseURL
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == c.host {
		return c.BaseURL
	}
	u.Host = c.host
	return u.String()
}

// Download fetches one subtitle file's bytes. Downloads count against the
// account's daily quota.
func (c *Client) Download(ctx context.Context, fileID int) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	var resp struct {
		Link      string `json:"link"`
		Remaining int    `json:"remaining"`
		Message   string `json:"message"`
	}
	err := c.do(ctx, http.MethodPost, c.apiBase()+"/download", map[string]int{"file_id": fileID}, true, &resp)
	if errors.Is(err, ErrAuth) && c.TokenFile != "" {
		// A cached token may have been revoked; log in again once.
		os.Remove(c.TokenFile)
		c.token = ""
		if err = c.login(ctx); err == nil {
			err = c.do(ctx, http.MethodPost, c.apiBase()+"/download", map[string]int{"file_id": fileID}, true, &resp)
		}
	}
	if err != nil {
		return nil, err
	}
	if resp.Link == "" {
		return nil, fmt.Errorf("OpenSubtitles returned no download link: %s", resp.Message)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resp.Link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	r, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &napi.TemporaryError{Err: err}
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subtitle file download: HTTP %d", r.StatusCode)
	}
	return io.ReadAll(io.LimitReader(r.Body, 16<<20))
}

// UserInfo logs in and reports the account's download allowance.
type UserInfo struct {
	Level              string
	AllowedDownloads   int
	RemainingDownloads int
}

func (c *Client) UserInfo(ctx context.Context) (*UserInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Level              string `json:"level"`
			AllowedDownloads   num    `json:"allowed_downloads"`
			RemainingDownloads num    `json:"remaining_downloads"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, c.apiBase()+"/infos/user", nil, true, &resp); err != nil {
		return nil, err
	}
	return &UserInfo{Level: resp.Data.Level, AllowedDownloads: int(resp.Data.AllowedDownloads),
		RemainingDownloads: int(resp.Data.RemainingDownloads)}, nil
}

// Feature is a movie or episode in the OpenSubtitles catalogue.
type Feature struct {
	IMDbID        int
	Title         string
	OriginalTitle string
	AKA           []string // alternative titles, e.g. other languages
	Year          int
	Type          string
}

// Names lists every title the feature is known by.
func (f *Feature) Names() []string {
	return append([]string{f.Title, f.OriginalTitle}, f.AKA...)
}

// Feature looks a movie up by IMDb ID, for its original and alternative
// titles (search results carry only the English one).
func (c *Client) Feature(ctx context.Context, imdbID int) (*Feature, error) {
	var resp struct {
		Data []struct {
			Attributes struct {
				Title         string   `json:"title"`
				OriginalTitle string   `json:"original_title"`
				AKA           []string `json:"title_aka"`
				Year          num      `json:"year"`
				IMDbID        num      `json:"imdb_id"`
				Type          string   `json:"feature_type"`
			} `json:"attributes"`
		} `json:"data"`
	}
	u := c.BaseURL + "/features?imdb_id=" + strconv.Itoa(imdbID)
	if err := c.do(ctx, http.MethodGet, u, nil, false, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("no OpenSubtitles feature tt%07d", imdbID)
	}
	a := resp.Data[0].Attributes
	return &Feature{IMDbID: int(a.IMDbID), Title: a.Title, OriginalTitle: a.OriginalTitle,
		AKA: a.AKA, Year: int(a.Year), Type: a.Type}, nil
}
