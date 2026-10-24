package subs

import (
	"regexp"
	"strings"
)

var (
	reSDHBracket = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)
	reSpeaker    = regexp.MustCompile(`^-?\s*[A-Z][A-Za-z .'-]{0,24}:\s*`)
	reTagsAll    = regexp.MustCompile(`</?[ibu]>`)
	reMusicOnly  = regexp.MustCompile(`^[♪♫#\s-]*$`)
)

// StripSDH removes what subtitles for the deaf and hard of hearing add to
// the dialogue: sound descriptions ([Dramatic music plays], (sighs)), music
// lines (♪ …) and speaker labels (JOEL:). Cues left without dialogue are
// dropped. A reference stripped this way lines up with ordinary subtitles.
func StripSDH(cues []Cue) []Cue {
	var out []Cue
	for _, c := range cues {
		var lines []string
		for _, l := range strings.Split(reTagsAll.ReplaceAllString(c.Text, ""), "\n") {
			l = strings.TrimSpace(reSDHBracket.ReplaceAllString(l, ""))
			l = strings.TrimSpace(reSpeaker.ReplaceAllString(l, ""))
			if strings.HasPrefix(l, "♪") || strings.HasSuffix(l, "♪") || reMusicOnly.MatchString(l) {
				continue
			}
			lines = append(lines, l)
		}
		if len(lines) > 0 {
			c.Text = strings.Join(lines, "\n")
			out = append(out, c)
		}
	}
	return out
}
