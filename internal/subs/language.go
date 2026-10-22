package subs

import (
	"strings"
	"unicode"
)

var (
	polishWords  = wordSet("nie się jest to że na co jak ale tak już mnie mi ty go jej tu czy był jestem wiem może dobrze tylko teraz")
	englishWords = wordSet("the you and is it that to of what in this don't i'm we have be not was are for your with know")
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// DetectLanguage guesses whether subtitle text is Polish ("PL") or English
// ("ENG"), or returns "" when it cannot tell. It is meant for subtitle
// files whose name carries no language tag.
func DetectLanguage(text string) string {
	var pl, en, words int
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	}) {
		words++
		if polishWords[f] {
			pl++
		}
		if englishWords[f] {
			en++
		}
		if strings.ContainsAny(f, "ąćęłńśźż") {
			pl += 2
		}
		if words > 4000 {
			break
		}
	}
	switch {
	case words < 20:
		return ""
	case pl > 2*en && pl >= words/50:
		return "PL"
	case en > 2*pl && en >= words/50:
		return "ENG"
	}
	return ""
}
