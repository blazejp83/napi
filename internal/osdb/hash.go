// Package osdb is a client for the OpenSubtitles.com REST API.
package osdb

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const hashChunk = 64 * 1024

// FileHash computes the OpenSubtitles "moviehash": the file size plus the
// sum of the first and last 64 KiB read as little-endian uint64 words,
// formatted as 16 hex digits.
func FileHash(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := fi.Size()
	if size < hashChunk {
		return "", size, fmt.Errorf("file too small for an OpenSubtitles hash")
	}
	h := uint64(size)
	buf := make([]byte, hashChunk)
	for _, off := range []int64{0, size - hashChunk} {
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return "", size, err
		}
		for i := 0; i < hashChunk; i += 8 {
			h += binary.LittleEndian.Uint64(buf[i:])
		}
	}
	return fmt.Sprintf("%016x", h), size, nil
}
