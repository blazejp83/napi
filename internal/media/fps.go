// Package media reads the video frame rate from container headers, which
// MicroDVD subtitles (timed in frames) need for conversion to SRT.
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

	"github.com/blaze/napi/internal/proc"
)

var errNoFPS = errors.New("frame rate not found")

// FrameRate returns the video frame rate of a file and where it came from
// ("mkv", "mp4", "avi" or "ffprobe").
func FrameRate(path string) (float64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	var magic [12]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return 0, "", errNoFPS
	}
	var fps float64
	var src string
	switch {
	case bytes.Equal(magic[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		fps, err = mkvFPS(f)
		src = "mkv"
	case string(magic[4:8]) == "ftyp" || string(magic[4:8]) == "moov":
		fps, err = mp4FPS(f)
		src = "mp4"
	case string(magic[:4]) == "RIFF" && string(magic[8:12]) == "AVI ":
		fps, err = aviFPS(f)
		src = "avi"
	default:
		err = errNoFPS
	}
	if err == nil && fps > 0 {
		return Snap(fps), src, nil
	}
	if fps, err := ffprobeFPS(path); err == nil {
		return Snap(fps), "ffprobe", nil
	}
	return 0, "", errNoFPS
}

// Snap rounds a measured frame rate to the standard rate it approximates.
func Snap(fps float64) float64 {
	for _, std := range []float64{24000.0 / 1001, 24, 25, 30000.0 / 1001, 30, 50, 60000.0 / 1001, 60} {
		if math.Abs(fps-std)/std < 0.002 {
			return std
		}
	}
	return fps
}

// ---- Matroska ----

const (
	ebmlSegment         = 0x18538067
	ebmlTracks          = 0x1654AE6B
	ebmlTrackEntry      = 0xAE
	ebmlTrackType       = 0x83
	ebmlDefaultDuration = 0x23E383
	ebmlCluster         = 0x1F43B675
)

type ebmlReader struct {
	r   io.ReadSeeker
	pos int64
}

func (e *ebmlReader) vint(keepMarker bool) (uint64, int, error) {
	var b [1]byte
	if _, err := io.ReadFull(e.r, b[:]); err != nil {
		return 0, 0, err
	}
	e.pos++
	n := 1
	for mask := byte(0x80); n <= 8 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 {
		return 0, 0, errNoFPS
	}
	v := uint64(b[0])
	if !keepMarker {
		v &= uint64(0xFF >> n)
	}
	allOnes := v == uint64(0xFF>>n)
	for i := 1; i < n; i++ {
		if _, err := io.ReadFull(e.r, b[:]); err != nil {
			return 0, 0, err
		}
		e.pos++
		v = v<<8 | uint64(b[0])
		if b[0] != 0xFF {
			allOnes = false
		}
	}
	if !keepMarker && allOnes {
		return math.MaxUint64, n, nil // unknown size
	}
	return v, n, nil
}

func (e *ebmlReader) header() (id, size uint64, err error) {
	if id, _, err = e.vint(true); err != nil {
		return
	}
	size, _, err = e.vint(false)
	return
}

func (e *ebmlReader) skip(n uint64) error {
	_, err := e.r.Seek(int64(n), io.SeekCurrent)
	e.pos += int64(n)
	return err
}

func (e *ebmlReader) uint(size uint64) (uint64, error) {
	if size > 8 {
		return 0, errNoFPS
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(e.r, buf); err != nil {
		return 0, err
	}
	e.pos += int64(size)
	var v uint64
	for _, b := range buf {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func mkvFPS(f io.ReadSeeker) (float64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	e := &ebmlReader{r: f}
	// EBML header, then Segment.
	if _, size, err := e.header(); err != nil || e.skip(size) != nil {
		return 0, errNoFPS
	}
	id, segSize, err := e.header()
	if err != nil || id != ebmlSegment {
		return 0, errNoFPS
	}
	segEnd := int64(math.MaxInt64)
	if segSize != math.MaxUint64 {
		segEnd = e.pos + int64(segSize)
	}
	for e.pos < segEnd {
		id, size, err := e.header()
		if err != nil || id == ebmlCluster || size == math.MaxUint64 {
			return 0, errNoFPS // tracks always precede the first cluster
		}
		if id != ebmlTracks {
			if e.skip(size) != nil {
				return 0, errNoFPS
			}
			continue
		}
		tracksEnd := e.pos + int64(size)
		for e.pos < tracksEnd {
			id, size, err := e.header()
			if err != nil {
				return 0, errNoFPS
			}
			if id != ebmlTrackEntry {
				e.skip(size)
				continue
			}
			entryEnd := e.pos + int64(size)
			var trackType, defDur uint64
			for e.pos < entryEnd {
				id, size, err := e.header()
				if err != nil {
					return 0, errNoFPS
				}
				switch id {
				case ebmlTrackType:
					trackType, _ = e.uint(size)
				case ebmlDefaultDuration:
					defDur, _ = e.uint(size)
				default:
					e.skip(size)
				}
			}
			if trackType == 1 && defDur > 0 {
				return 1e9 / float64(defDur), nil
			}
		}
		return 0, errNoFPS
	}
	return 0, errNoFPS
}

// ---- MP4 / QuickTime ----

type box struct {
	typ        string
	start, end int64 // payload range
}

// boxes lists the child boxes within [start, end).
func boxes(r io.ReadSeeker, start, end int64) ([]box, error) {
	var out []box
	for pos := start; pos+8 <= end; {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		var h [16]byte
		if _, err := io.ReadFull(r, h[:8]); err != nil {
			return out, nil
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		hdr := int64(8)
		switch size {
		case 0:
			size = end - pos
		case 1:
			if _, err := io.ReadFull(r, h[8:16]); err != nil {
				return out, nil
			}
			size = int64(binary.BigEndian.Uint64(h[8:16]))
			hdr = 16
		}
		if size < hdr || pos+size > end {
			return out, nil
		}
		out = append(out, box{typ: string(h[4:8]), start: pos + hdr, end: pos + size})
		pos += size
	}
	return out, nil
}

func child(r io.ReadSeeker, parent box, typ string) (box, bool) {
	kids, _ := boxes(r, parent.start, parent.end)
	for _, b := range kids {
		if b.typ == typ {
			return b, true
		}
	}
	return box{}, false
}

func readAt(r io.ReadSeeker, off int64, n int) ([]byte, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func mp4FPS(f io.ReadSeeker) (float64, error) {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	moov, ok := child(f, box{start: 0, end: end}, "moov")
	if !ok {
		return 0, errNoFPS
	}
	traks, _ := boxes(f, moov.start, moov.end)
	for _, trak := range traks {
		if trak.typ != "trak" {
			continue
		}
		mdia, ok := child(f, trak, "mdia")
		if !ok {
			continue
		}
		hdlr, ok := child(f, mdia, "hdlr")
		if !ok {
			continue
		}
		if h, err := readAt(f, hdlr.start+8, 4); err != nil || string(h) != "vide" {
			continue
		}
		mdhd, ok := child(f, mdia, "mdhd")
		if !ok {
			continue
		}
		v, err := readAt(f, mdhd.start, 1)
		if err != nil {
			continue
		}
		tsOff := mdhd.start + 12
		if v[0] == 1 {
			tsOff = mdhd.start + 20
		}
		ts, err := readAt(f, tsOff, 4)
		if err != nil {
			continue
		}
		timescale := float64(binary.BigEndian.Uint32(ts))

		minf, ok := child(f, mdia, "minf")
		if !ok {
			continue
		}
		stbl, ok := child(f, minf, "stbl")
		if !ok {
			continue
		}
		stts, ok := child(f, stbl, "stts")
		if !ok {
			continue
		}
		cnt, err := readAt(f, stts.start+4, 4)
		if err != nil {
			continue
		}
		n := binary.BigEndian.Uint32(cnt)
		if n == 0 || int64(n)*8 > stts.end-stts.start {
			continue
		}
		entries, err := readAt(f, stts.start+8, int(n)*8)
		if err != nil {
			continue
		}
		var samples, duration float64
		for i := 0; i < int(n); i++ {
			c := float64(binary.BigEndian.Uint32(entries[i*8:]))
			d := float64(binary.BigEndian.Uint32(entries[i*8+4:]))
			samples += c
			duration += c * d
		}
		if duration > 0 && timescale > 0 {
			return samples * timescale / duration, nil
		}
	}
	return 0, errNoFPS
}

// ---- AVI ----

func aviFPS(f io.ReadSeeker) (float64, error) {
	// RIFF 'AVI ' > LIST 'hdrl' > 'avih', LIST 'strl' > 'strh' (fccType 'vids').
	buf, err := readAt(f, 12, 64*1024)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	for off := 0; ; {
		i := bytes.Index(buf[off:], []byte("strh"))
		if i < 0 {
			break
		}
		i += off
		off = i + 4
		// chunk: 'strh' size fccType fccHandler dwFlags wPriority wLanguage dwInitialFrames dwScale dwRate
		if i+8+28 > len(buf) || string(buf[i+8:i+12]) != "vids" {
			continue
		}
		h := buf[i+8:]
		scale := binary.LittleEndian.Uint32(h[20:])
		rate := binary.LittleEndian.Uint32(h[24:])
		if scale > 0 && rate > 0 {
			return float64(rate) / float64(scale), nil
		}
	}
	if i := bytes.Index(buf, []byte("avih")); i >= 0 && i+12 <= len(buf) {
		usPerFrame := binary.LittleEndian.Uint32(buf[i+8:])
		if usPerFrame > 0 {
			return 1e6 / float64(usPerFrame), nil
		}
	}
	return 0, errNoFPS
}

// ---- ffprobe fallback ----

func ffprobeFPS(path string) (float64, error) {
	exe, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate", "-of", "csv=p=0", filepath.Clean(path))
	proc.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	num, den, ok := strings.Cut(strings.TrimSpace(string(out)), "/")
	n, err1 := strconv.ParseFloat(num, 64)
	d := 1.0
	var err2 error
	if ok {
		d, err2 = strconv.ParseFloat(den, 64)
	}
	if err1 != nil || err2 != nil || n <= 0 || d <= 0 {
		return 0, errNoFPS
	}
	return n / d, nil
}
