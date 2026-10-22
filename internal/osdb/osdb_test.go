package osdb

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v.bin")
	data := make([]byte, 300_001)
	rand.New(rand.NewSource(1)).Read(data)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, size, err := FileHash(p)
	if err != nil || size != int64(len(data)) {
		t.Fatal(err, size)
	}
	// Reference implementation from the OpenSubtitles wiki, in Python.
	py := `import struct,os,sys
f=open(sys.argv[1],'rb');s=os.path.getsize(sys.argv[1]);h=s
for off in (0,s-65536):
    f.seek(off);b=f.read(65536)
    for (x,) in struct.iter_unpack('<Q',b): h=(h+x)&0xFFFFFFFFFFFFFFFF
print('%016x'%h)`
	out, err := exec.Command("python3", "-I", "-c", py, p).Output()
	if err != nil {
		t.Skip("python3 not available")
	}
	if want := strings.TrimSpace(string(out)); got != want {
		t.Fatalf("hash %s, want %s", got, want)
	}
}

func TestGuessTitle(t *testing.T) {
	for in, want := range map[string]Guess{
		`Z:\Filmy\4 Months, 3 Weeks and 2 Days (2007) (1080p x265 Silence).mkv`: {Title: "4 Months, 3 Weeks and 2 Days", Year: 2007},
		"The.Big.Lebowski.1998.1080p.BluRay.x264-AMIABLE.mkv":                   {Title: "The Big Lebowski", Year: 1998},
		"Blade Runner 2049 (2017).mkv":                                          {Title: "Blade Runner 2049", Year: 2017},
		"All.Her.Fault.S01E04.1080p.WEB.H264-SYLiX.mkv":                         {Title: "All Her Fault", Season: 1, Episode: 4},
		"Show.Name.2019.S02E15.720p.WEB.h264.mkv":                               {Title: "Show Name", Year: 2019, Season: 2, Episode: 15},
		"Show Name (2025) - s1e3.mkv":                                           {Title: "Show Name", Year: 2025, Season: 1, Episode: 3},
		"some_movie_1080p_x265.mp4":                                             {Title: "some movie"},
	} {
		if got := GuessTitle(filepath.FromSlash(strings.ReplaceAll(in, `\`, "/"))); got != want {
			t.Errorf("GuessTitle(%q) = %+v; want %+v", in, got, want)
		}
	}
}

func TestRank(t *testing.T) {
	f := []File{{ID: 1}}
	video := "4 Months, 3 Weeks and 2 Days (2007) (1080p BluRay x265 Silence).mkv"
	cands := []Subtitle{
		{ID: "dvd", Language: "pl", Release: "4.Months.3.Weeks.2.Days.DVDRip.XviD", FPS: 25, DownloadCount: 5000, Files: f},
		{ID: "bd", Language: "pl", Release: "4.Months.3.Weeks.and.2.Days.2007.1080p.BluRay.x264", FPS: 23.976, DownloadCount: 300, Files: f},
		{ID: "ai", Language: "pl", Release: "4 Months 3 Weeks 2 Days 1080p BluRay x265 Silence", FPS: 23.976, AITranslated: true, Files: f},
		{ID: "en", Language: "en", Release: "4.Months.3.Weeks.and.2.Days.1080p.BluRay.x265-Silence", FPS: 23.976, Files: f},
		{ID: "cd", Language: "pl", Release: "1080p BluRay", Files: []File{{ID: 2}, {ID: 3}}},
		{ID: "foreign", Language: "pl", Release: "1080p BluRay x265", ForeignPartsOnly: true, Files: f},
		{ID: "de", Language: "de", Release: "1080p BluRay x265 Silence", Files: f},
	}
	got := Rank(cands, video, 23.976, []string{"pl", "en"})
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "bd,dvd,ai,en" {
		t.Fatalf("rank order %v", ids)
	}
}

func TestClient(t *testing.T) {
	var logins, downloads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/file/") && (r.Header.Get("Api-Key") != "key" || r.Header.Get("User-Agent") != "napi v9") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v1/subtitles":
			if q := r.URL.RawQuery; q != "imdb_id=1032846&languages=pl%2Cen&moviehash=00ff" &&
				q != "episode_number=4&languages=pl&parent_imdb_id=31314751&season_number=1" {
				t.Errorf("query %q", q)
			}
			w.Write([]byte(`{"data":[{"id":"7","attributes":{"language":"pl","release":"R","fps":"23.976",
				"download_count":12,"moviehash_match":true,"feature_details":{"imdb_id":1032846,"title":"T"},
				"files":[{"file_id":99,"file_name":"r.srt"}]}}]}`))
		case "/api/v1/login":
			logins++
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["username"] != "u" || b["password"] != "p" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"token":"tok","base_url":"` + r.Host + `"}`))
		case "/api/v1/download":
			downloads++
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if downloads > 1 {
				w.WriteHeader(http.StatusNotAcceptable)
				w.Write([]byte(`{"message":"You have downloaded your allowed 20 subtitles for 24h"}`))
				return
			}
			w.Write([]byte(`{"link":"http://` + r.Host + `/file/r.srt","remaining":19}`))
		case "/file/r.srt":
			w.Write([]byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nx\r\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient("key", "u", "p", "napi v9")
	c.BaseURL = srv.URL + "/api/v1"
	c.TokenFile = filepath.Join(t.TempDir(), "token.json")
	ctx := context.Background()

	hits, err := c.Search(ctx, Query{MovieHash: "00FF", IMDbID: 1032846, Text: "ignored", Languages: []string{"pl", "en"}})
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	h := hits[0]
	if h.FPS != 23.976 || !h.MovieHashMatch || h.IMDbID != 1032846 || h.Files[0].ID != 99 || h.DownloadCount != 12 {
		t.Fatalf("parsed %+v", h)
	}
	if _, err := c.Search(ctx, Query{IMDbID: 31314751, Season: 1, Episode: 4, Languages: []string{"pl"}}); err != nil {
		t.Fatal(err)
	}
	data, err := c.Download(ctx, 99)
	if err != nil || !strings.Contains(string(data), "00:00:01,000") {
		t.Fatal(string(data), err)
	}
	if _, err := c.Download(ctx, 99); !errors.Is(err, ErrQuota) {
		t.Fatalf("second download: %v, want ErrQuota", err)
	}
	// A new client reuses the cached token instead of logging in again.
	c2 := NewClient("key", "u", "p", "napi v9")
	c2.BaseURL, c2.TokenFile = c.BaseURL, c.TokenFile
	c2.Download(ctx, 99)
	if logins != 1 {
		t.Fatalf("logins = %d, want 1", logins)
	}
}

func TestNormTitle(t *testing.T) {
	for in, want := range map[string]string{"À nos amours": "a nos amours", "Łódź & Co.": "lodz and co", "Amélie!": "amelie"} {
		if got := NormTitle(in); got != want {
			t.Errorf("NormTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
