package napi

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultEndpoint = "https://napiprojekt.pl/api/api-napiprojekt3.php"
	// DefaultClient is the client name sent to the API. The server only answers
	// whitelisted names; this is the one used by open-source NapiProjekt clients.
	DefaultClient = "NapiProjektPython"
	userAgent     = "napi-go/0.1 (+https://github.com/blaze/napi)"
)

// ErrNotFound means the database has no subtitles for the hash and language.
var ErrNotFound = errors.New("subtitles not found")

// Subtitles is one downloaded subtitle file, still in its original encoding and format.
type Subtitles struct {
	Hash     string
	Lang     string
	Content  []byte
	Author   string
	Uploader string
	Uploaded string
}

// Client talks to api-napiprojekt3.php.
type Client struct {
	Endpoint   string
	ClientName string
	HTTP       *http.Client
}

func NewClient(clientName string) *Client {
	if clientName == "" {
		clientName = DefaultClient
	}
	return &Client{
		Endpoint:   DefaultEndpoint,
		ClientName: clientName,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
}

type apiResult struct {
	Status    string `xml:"status"`
	Subtitles struct {
		ID         string `xml:"id"`
		Content    string `xml:"content"`
		Author     string `xml:"author"`
		Uploader   string `xml:"uploader"`
		UploadDate string `xml:"upload_date"`
	} `xml:"subtitles"`
}

// TemporaryError marks failures worth retrying (network errors, 5xx).
type TemporaryError struct{ Err error }

func (e *TemporaryError) Error() string { return e.Err.Error() }
func (e *TemporaryError) Unwrap() error { return e.Err }

// Download fetches subtitles for a file hash in one language ("PL" or "ENG").
func (c *Client) Download(ctx context.Context, hash, lang string) (*Subtitles, error) {
	form := url.Values{
		"mode":                      {"17"},
		"client":                    {c.ClientName},
		"downloaded_subtitles_id":   {hash},
		"downloaded_subtitles_txt":  {"1"},
		"downloaded_subtitles_lang": {lang},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &TemporaryError{err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, &TemporaryError{err}
	}
	if resp.StatusCode >= 500 {
		return nil, &TemporaryError{fmt.Errorf("server returned HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	return parseResponse(body, hash, lang)
}

func parseResponse(body []byte, hash, lang string) (*Subtitles, error) {
	var r apiResult
	if err := xml.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("invalid API response: %w", err)
	}
	// Unknown hashes come back with no status at all; "failed" covers both a
	// miss and a rejected client name, which the API does not distinguish.
	if r.Status != "success" || strings.TrimSpace(r.Subtitles.Content) == "" {
		return nil, ErrNotFound
	}
	content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.Subtitles.Content))
	if err != nil {
		return nil, fmt.Errorf("invalid subtitle content: %w", err)
	}
	return &Subtitles{
		Hash:     hash,
		Lang:     lang,
		Content:  content,
		Author:   r.Subtitles.Author,
		Uploader: r.Subtitles.Uploader,
		Uploaded: r.Subtitles.UploadDate,
	}, nil
}
