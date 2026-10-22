package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blazejp83/napi/internal/proc"
)

var errNoDuration = errors.New("duration not found")

const (
	ebmlInfo          = 0x1549A966
	ebmlTimecodeScale = 0x2AD7B1
	ebmlDuration      = 0x4489
)

// Duration returns the playing time of a video file from its container
// header, falling back to ffprobe.
func Duration(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var magic [12]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return 0, errNoDuration
	}
	var d time.Duration
	switch {
	case bytes.Equal(magic[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		d, err = mkvDuration(f)
	case string(magic[4:8]) == "ftyp" || string(magic[4:8]) == "moov":
		d, err = mp4Duration(f)
	case string(magic[:4]) == "RIFF" && string(magic[8:12]) == "AVI ":
		d, err = aviDuration(f)
	default:
		err = errNoDuration
	}
	if err == nil && d > 0 {
		return d, nil
	}
	return ffprobeDuration(path)
}

func mkvDuration(f io.ReadSeeker) (time.Duration, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	e := &ebmlReader{r: f}
	if _, size, err := e.header(); err != nil || e.skip(size) != nil {
		return 0, errNoDuration
	}
	id, segSize, err := e.header()
	if err != nil || id != ebmlSegment {
		return 0, errNoDuration
	}
	segEnd := int64(math.MaxInt64)
	if segSize != math.MaxUint64 {
		segEnd = e.pos + int64(segSize)
	}
	for e.pos < segEnd {
		id, size, err := e.header()
		if err != nil || id == ebmlCluster || size == math.MaxUint64 {
			return 0, errNoDuration
		}
		if id != ebmlInfo {
			if e.skip(size) != nil {
				return 0, errNoDuration
			}
			continue
		}
		end := e.pos + int64(size)
		scale, dur := uint64(1_000_000), 0.0
		for e.pos < end {
			id, size, err := e.header()
			if err != nil {
				return 0, errNoDuration
			}
			switch id {
			case ebmlTimecodeScale:
				scale, _ = e.uint(size)
			case ebmlDuration:
				dur, _ = e.float(size)
			default:
				e.skip(size)
			}
		}
		if dur <= 0 {
			return 0, errNoDuration
		}
		return time.Duration(dur * float64(scale)), nil
	}
	return 0, errNoDuration
}

func (e *ebmlReader) float(size uint64) (float64, error) {
	v, err := e.uint(size)
	if err != nil {
		return 0, err
	}
	switch size {
	case 4:
		return float64(math.Float32frombits(uint32(v))), nil
	case 8:
		return math.Float64frombits(v), nil
	}
	return 0, errNoDuration
}

func mp4Duration(f io.ReadSeeker) (time.Duration, error) {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	moov, ok := child(f, box{start: 0, end: end}, "moov")
	if !ok {
		return 0, errNoDuration
	}
	mvhd, ok := child(f, moov, "mvhd")
	if !ok {
		return 0, errNoDuration
	}
	h, err := readAt(f, mvhd.start, 32)
	if err != nil {
		return 0, errNoDuration
	}
	var scale, dur float64
	if h[0] == 1 {
		scale = float64(binary.BigEndian.Uint32(h[20:]))
		dur = float64(binary.BigEndian.Uint64(h[24:]))
	} else {
		scale = float64(binary.BigEndian.Uint32(h[12:]))
		dur = float64(binary.BigEndian.Uint32(h[16:]))
	}
	if scale <= 0 || dur <= 0 {
		return 0, errNoDuration
	}
	return time.Duration(dur / scale * float64(time.Second)), nil
}

func aviDuration(f io.ReadSeeker) (time.Duration, error) {
	buf, err := readAt(f, 12, 64*1024)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	// avih: dwMicroSecPerFrame, dwMaxBytesPerSec, dwPaddingGranularity, dwFlags, dwTotalFrames
	i := bytes.Index(buf, []byte("avih"))
	if i < 0 || i+8+20 > len(buf) {
		return 0, errNoDuration
	}
	h := buf[i+8:]
	us := binary.LittleEndian.Uint32(h[0:])
	frames := binary.LittleEndian.Uint32(h[16:])
	if us == 0 || frames == 0 {
		return 0, errNoDuration
	}
	return time.Duration(us) * time.Microsecond * time.Duration(frames), nil
}

func ffprobeDuration(path string) (time.Duration, error) {
	exe, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, errNoDuration
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-v", "error", "-show_entries", "format=duration",
		"-of", "csv=p=0", filepath.Clean(path))
	proc.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, errNoDuration
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || sec <= 0 {
		return 0, errNoDuration
	}
	return time.Duration(sec * float64(time.Second)), nil
}
