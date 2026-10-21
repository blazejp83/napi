// Package napi implements the NapiProjekt (napiprojekt.pl) subtitle API.
package napi

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"os"
)

// HashSize is how much of the video file NapiProjekt hashes: the first 10 MiB.
const HashSize = 10 * 1024 * 1024

// FileHash returns the NapiProjekt identifier of a video file: the lowercase
// hex MD5 of its first 10 MiB (or of the whole file if it is smaller).
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return ReaderHash(f)
}

// ReaderHash hashes the first HashSize bytes of r.
func ReaderHash(r io.Reader) (string, error) {
	h := md5.New()
	if _, err := io.CopyN(h, r, HashSize); err != nil && err != io.EOF {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
