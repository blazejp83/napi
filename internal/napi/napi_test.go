package napi

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestReaderHashUsesFirst10MiB(t *testing.T) {
	data := bytes.Repeat([]byte("napi"), HashSize/4+1000)
	sum := md5.Sum(data[:HashSize])
	got, err := ReaderHash(bytes.NewReader(data))
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("got %s, %v", got, err)
	}
	small := []byte("short file")
	sum = md5.Sum(small)
	if got, _ := ReaderHash(bytes.NewReader(small)); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("small file: got %s", got)
	}
}

const okResponse = `<?xml version="1.0"?>
<result><status>success</status><subtitles><id>abc</id><author>brak</author><uploader>u</uploader>
<content><![CDATA[ezF9ezJ9VGVzdA==]]></content></subtitles><response_time>0.1 s.</response_time></result>`

func TestDownload(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		got = map[string]string{}
		for k := range r.PostForm {
			got[k] = r.PostForm.Get(k)
		}
		if got["downloaded_subtitles_id"] == "missing" {
			w.Write([]byte(`<?xml version="1.0"?><result><response_time>0.1 s.</response_time></result>`))
			return
		}
		w.Write([]byte(okResponse))
	}))
	defer srv.Close()
	c := NewClient("")
	c.Endpoint = srv.URL

	sub, err := c.Download(context.Background(), "abc", "PL")
	if err != nil {
		t.Fatal(err)
	}
	if string(sub.Content) != "{1}{2}Test" || sub.Uploader != "u" {
		t.Fatalf("unexpected subtitles %+v", sub)
	}
	if got["mode"] != "17" || got["client"] != DefaultClient || got["downloaded_subtitles_lang"] != "PL" || got["downloaded_subtitles_txt"] != "1" {
		t.Fatalf("unexpected form %v", got)
	}
	if _, err := c.Download(context.Background(), "missing", "PL"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing hash: got %v", err)
	}
}

func TestDownloadServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient("")
	c.Endpoint = srv.URL
	var tmp *TemporaryError
	if _, err := c.Download(context.Background(), "abc", "PL"); !errors.As(err, &tmp) {
		t.Fatalf("got %v, want TemporaryError", err)
	}
}

// TestLive talks to napiprojekt.pl. Run with NAPI_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("NAPI_LIVE") == "" {
		t.Skip("set NAPI_LIVE=1 to query napiprojekt.pl")
	}
	sub, err := NewClient("").Download(context.Background(), "6303e7ee6a835e9fcede9fb2fb00cb36", "PL")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Content) < 1000 {
		t.Fatalf("suspiciously short subtitles: %d bytes", len(sub.Content))
	}
}

func TestMovieInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("mode") != "2" || r.PostForm.Get("downloaded_cover_id") != "known" {
			w.Write([]byte(`<?xml version="1.0"?><result><response_time>0.1 s.</response_time></result>`))
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><result><movie><status>success</status><id>80c5</id>
<title><![CDATA[4 miesiące, 3 tygodnie i 2 dni]]></title><other_titles><other_0><![CDATA[4 Months, 3 Weeks and 2 Days]]></other_0></other_titles>
<year>2007</year><direct_links><imdb_com><![CDATA[https://www.imdb.com/title/tt1032846/]]></imdb_com></direct_links>
<cover><![CDATA[/9j/tt8]]></cover></movie></result>`))
	}))
	defer srv.Close()
	c := NewClient("")
	c.Endpoint = srv.URL
	m, err := c.MovieInfo(context.Background(), "known")
	if err != nil {
		t.Fatal(err)
	}
	if m.IMDbID != 1032846 || m.Year != 2007 || m.Title != "4 miesiące, 3 tygodnie i 2 dni" || len(m.Other) != 1 {
		t.Fatalf("%+v", m)
	}
	if _, err := c.MovieInfo(context.Background(), "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}
