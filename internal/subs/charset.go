// Package subs decodes, parses and converts the subtitle files NapiProjekt serves.
package subs

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

const polishLetters = "ąćęłńóśźżĄĆĘŁŃÓŚŹŻ"

// Decode converts raw subtitle bytes to a Go string and reports the detected
// charset: "utf-8", "windows-1250" or "iso-8859-2". Polish subtitles in the
// database are mostly windows-1250, some are UTF-8 and a few ISO-8859-2.
func Decode(b []byte) (string, string) {
	b = bytes.TrimPrefix(b, utf8BOM)
	if utf8.Valid(b) {
		return string(b), "utf-8"
	}
	cp, _ := charmap.Windows1250.NewDecoder().Bytes(b)
	iso, _ := charmap.ISO8859_2.NewDecoder().Bytes(b)
	if score(string(iso)) > score(string(cp)) {
		return string(iso), "iso-8859-2"
	}
	return string(cp), "windows-1250"
}

// score rewards Polish letters and penalises characters that only appear when
// text is decoded with the wrong code page (controls, ±, ¶, Ľ ...).
func score(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case strings.ContainsRune(polishLetters, r):
			n += 2
		case r == '\n' || r == '\r' || r == '\t':
		case unicode.IsControl(r):
			n -= 5
		case r > 0x7F && !unicode.IsLetter(r) && !strings.ContainsRune("„”–—…«»°", r):
			n -= 3
		}
	}
	return n
}

// Encodings selectable for output files.
const (
	EncUTF8    = "utf8"
	EncUTF8BOM = "utf8bom"
	EncCP1250  = "cp1250"
)

// Encode converts text to the requested output encoding. Characters that
// windows-1250 cannot represent are replaced with '?'.
func Encode(s, enc string) []byte {
	switch enc {
	case EncUTF8:
		return []byte(s)
	case EncCP1250:
		var buf bytes.Buffer
		e := charmap.Windows1250
		for _, r := range s {
			if b, ok := e.EncodeRune(r); ok {
				buf.WriteByte(b)
			} else {
				buf.WriteByte('?')
			}
		}
		return buf.Bytes()
	default:
		return append(append([]byte{}, utf8BOM...), s...)
	}
}

// ValidEncoding reports whether enc is a supported output encoding name.
func ValidEncoding(enc string) bool {
	return enc == EncUTF8 || enc == EncUTF8BOM || enc == EncCP1250
}
