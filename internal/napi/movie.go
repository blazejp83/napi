package napi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Movie is what NapiProjekt knows about the film a video file belongs to.
// Users associate file hashes with movies, so this often works for files
// that have no subtitles of their own.
type Movie struct {
	ID     string
	Title  string
	Other  []string
	Year   int
	IMDbID int // numeric part of tt1234567, 0 if unknown
}

var reIMDb = regexp.MustCompile(`tt(\d{5,9})`)

// PageURL is the movie's page on napiprojekt.pl, which lists subtitles for
// all its versions (the link format the official client opens).
func (m *Movie) PageURL() string {
	return "https://www.napiprojekt.pl/index.php?dz=16&film=" + url.QueryEscape(m.ID)
}

// Name is the title with the year, e.g. "4 miesiące, 3 tygodnie i 2 dni (2007)".
func (m *Movie) Name() string {
	if m.Year > 0 {
		return fmt.Sprintf("%s (%d)", m.Title, m.Year)
	}
	return m.Title
}

// MovieInfo looks up the movie associated with a file hash (API mode 2).
// It returns ErrNotFound when the hash is not associated with a movie.
func (c *Client) MovieInfo(ctx context.Context, hash string) (*Movie, error) {
	form := url.Values{
		"mode":                {"2"},
		"client":              {c.ClientName},
		"downloaded_cover_id": {hash},
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &TemporaryError{err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &TemporaryError{fmt.Errorf("server returned HTTP %d", resp.StatusCode)}
	}
	var r struct {
		Movie struct {
			Status string `xml:"status"`
			ID     string `xml:"id"`
			Title  string `xml:"title"`
			Other  struct {
				Inner []struct {
					Value string `xml:",chardata"`
				} `xml:",any"`
			} `xml:"other_titles"`
			Year  string `xml:"year"`
			Links struct {
				IMDb string `xml:"imdb_com"`
			} `xml:"direct_links"`
		} `xml:"movie"`
	}
	if err := xml.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("invalid API response: %w", err)
	}
	if r.Movie.Status != "success" || r.Movie.ID == "" {
		return nil, ErrNotFound
	}
	m := &Movie{ID: r.Movie.ID, Title: strings.TrimSpace(r.Movie.Title)}
	m.Year, _ = strconv.Atoi(strings.TrimSpace(r.Movie.Year))
	for _, o := range r.Movie.Other.Inner {
		if v := strings.TrimSpace(o.Value); v != "" {
			m.Other = append(m.Other, v)
		}
	}
	if mm := reIMDb.FindStringSubmatch(r.Movie.Links.IMDb); mm != nil {
		m.IMDbID, _ = strconv.Atoi(mm[1])
	}
	return m, nil
}
