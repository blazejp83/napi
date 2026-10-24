// Package subsync aligns subtitles made for another release of a movie to a
// video file's audio, using alass (https://github.com/kaegi/alass).
package subsync

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blazejp83/napi/internal/proc"
)

// Pinned alass release. The Windows archive bundles ffmpeg and ffprobe.
const (
	alassVersion = "2.0.0"
	windowsURL   = "https://github.com/kaegi/alass/releases/download/v2.0.0/alass-windows64.zip"
	windowsSHA   = "e81a72f97f592910e909a2352d6b8c0de0801c51ac1383bad4ebf3f2ecdd2fd8"
	linuxURL     = "https://github.com/kaegi/alass/releases/download/v2.0.0/alass-linux64"
	linuxSHA     = "7bd0b9ae7e035d3ba940eacffb21243614df36231d47f21f0b4ce42001ab7fcd"
)

// Tools is a runnable alass with the environment it needs, and the ffmpeg
// and ffprobe it uses ("" when not available).
type Tools struct {
	Alass           string
	Env             []string
	FFmpeg, FFprobe string
}

var (
	ensureMu sync.Mutex
	ensured  *Tools
)

// Ensure returns usable tools: alassPath if set, an alass on PATH, or the
// pinned release, downloaded once into the user's cache folder.
func Ensure(ctx context.Context, alassPath string) (*Tools, error) {
	ensureMu.Lock()
	defer ensureMu.Unlock()
	if ensured != nil {
		return ensured, nil
	}
	t, err := find(alassPath)
	if err == nil {
		ensured = t
		return t, nil
	}
	if alassPath != "" {
		return nil, err
	}
	t, err = install(ctx)
	if err != nil {
		return nil, fmt.Errorf("installing alass: %w", err)
	}
	ensured = t
	return t, nil
}

func managedDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "napi", "tools", "alass-"+alassVersion), nil
}

func find(alassPath string) (*Tools, error) {
	if alassPath != "" {
		if _, err := os.Stat(alassPath); err != nil {
			return nil, fmt.Errorf("alassPath: %w", err)
		}
		return withPathFFmpeg(&Tools{Alass: alassPath}), nil
	}
	if dir, err := managedDir(); err == nil {
		if t := managedTools(dir); t != nil {
			return t, nil
		}
	}
	for _, name := range []string{"alass-cli", "alass"} {
		if p, err := exec.LookPath(name); err == nil {
			return withPathFFmpeg(&Tools{Alass: p}), nil
		}
	}
	return nil, errors.New("alass not found")
}

func managedTools(dir string) *Tools {
	if runtime.GOOS == "windows" {
		root := filepath.Join(dir, "alass-windows64")
		exe := filepath.Join(root, "bin", "alass-cli.exe")
		if _, err := os.Stat(exe); err != nil {
			return nil
		}
		ff := filepath.Join(root, "ffmpeg", "bin")
		t := &Tools{Alass: exe, FFmpeg: filepath.Join(ff, "ffmpeg.exe"), FFprobe: filepath.Join(ff, "ffprobe.exe")}
		t.Env = []string{"ALASS_FFMPEG_PATH=" + t.FFmpeg, "ALASS_FFPROBE_PATH=" + t.FFprobe}
		return t
	}
	exe := filepath.Join(dir, "alass-linux64")
	if _, err := os.Stat(exe); err != nil {
		return nil
	}
	return withPathFFmpeg(&Tools{Alass: exe})
}

// withPathFFmpeg fills in ffmpeg and ffprobe from PATH.
func withPathFFmpeg(t *Tools) *Tools {
	t.FFmpeg, _ = exec.LookPath("ffmpeg")
	t.FFprobe, _ = exec.LookPath("ffprobe")
	return t
}

func install(ctx context.Context) (*Tools, error) {
	var url, sum string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		url, sum = windowsURL, windowsSHA
	case "linux/amd64":
		url, sum = linuxURL, linuxSHA
	default:
		return nil, fmt.Errorf("no alass build for %s/%s; install it and set alassPath", runtime.GOOS, runtime.GOARCH)
	}
	dir, err := managedDir()
	if err != nil {
		return nil, err
	}
	data, err := fetch(ctx, url, sum)
	if err != nil {
		return nil, err
	}
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	if strings.HasSuffix(url, ".zip") {
		err = unzip(data, tmp)
	} else {
		err = os.WriteFile(filepath.Join(tmp, filepath.Base(url)), data, 0o755)
	}
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	if t := managedTools(dir); t != nil {
		return t, nil
	}
	return nil, errors.New("alass archive has an unexpected layout")
}

func fetch(ctx context.Context, url, wantSHA string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != wantSHA {
		return nil, fmt.Errorf("%s: checksum mismatch", url)
	}
	return data, nil
}

func unzip(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		p := filepath.Join(dest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(p, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Sync aligns srt (SubRip text) to ref and returns the corrected SubRip text.
// ref is either a subtitle file whose timing matches the video (fast and
// reliable: the lines are aligned to its lines) or the video itself, whose
// audio is then analysed for speech (slow; unreliable on films where music
// and effects drown the dialogue).
func (t *Tools) Sync(ctx context.Context, ref, srt string) (string, error) {
	dir, err := os.MkdirTemp("", "napi-sync-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in.srt"), filepath.Join(dir, "out.srt")
	if err := os.WriteFile(in, []byte(srt), 0o644); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.Alass, "--encoding-inc", "utf-8", ref, in, out)
	cmd.Env = append(os.Environ(), t.Env...)
	proc.HideWindow(cmd)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("alass: %v: %s", err, lastLines(log.String(), 3))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return "", fmt.Errorf("alass produced no output: %s", lastLines(log.String(), 3))
	}
	return string(b), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r", "\n")), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			keep = append([]string{l}, keep...)
		}
	}
	return strings.Join(keep, " | ")
}

// textSubtitleCodecs are embedded subtitle formats ffmpeg can turn into SRT
// (bitmap ones like PGS or VobSub cannot be).
var textSubtitleCodecs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true}

// Track is an embedded subtitle track.
type Track struct {
	Index int    // stream index in the file
	Lang  string // ISO 639-2 tag, "" if none
}

// TextTracks lists the embedded text subtitle tracks of a video.
func (t *Tools) TextTracks(ctx context.Context, video string) ([]Track, error) {
	if t.FFprobe == "" {
		return nil, errors.New("ffprobe not available")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.FFprobe, "-v", "error", "-select_streams", "s",
		"-show_entries", "stream=index,codec_name:stream_tags=language", "-of", "csv=p=0", video)
	proc.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var tracks []Track
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 2 || !textSubtitleCodecs[f[1]] {
			continue
		}
		var tr Track
		fmt.Sscanf(f[0], "%d", &tr.Index)
		if len(f) > 2 {
			tr.Lang = strings.ToLower(f[2])
		}
		tracks = append(tracks, tr)
	}
	return tracks, nil
}

// ExtractTrack writes an embedded text subtitle track to out as SRT. It reads
// through the whole file, like an audio sync.
func (t *Tools) ExtractTrack(ctx context.Context, video string, track Track, out string) error {
	if t.FFmpeg == "" {
		return errors.New("ffmpeg not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.FFmpeg, "-v", "error", "-y", "-i", video,
		"-map", fmt.Sprintf("0:%d", track.Index), "-c:s", "srt", out)
	proc.HideWindow(cmd)
	var log bytes.Buffer
	cmd.Stderr = &log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, lastLines(log.String(), 2))
	}
	return nil
}
