package subs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Format names.
const (
	MicroDVD = "microdvd"
	MPL2     = "mpl2"
	TMPlayer = "tmplayer"
	SubRip   = "srt"
	Unknown  = "unknown"
)

// Cue is one subtitle entry. Text lines are separated by "\n" and may contain
// <i>, <b> and <u> tags.
type Cue struct {
	Start, End time.Duration
	Text       string
}

var (
	reMicroDVD = regexp.MustCompile(`^\{(\d+)\}\{(\d*)\}(.*)$`)
	reMPL2     = regexp.MustCompile(`^\[(\d+)\]\[(\d*)\](.*)$`)
	reTMPlayer = regexp.MustCompile(`^(\d{1,2}):(\d{2}):(\d{2})(?:[.,]\d+)?[:=](.*)$`)
	reSRTTime  = regexp.MustCompile(`^(\d+):(\d{2}):(\d{2})[,.](\d{1,3})\s*-->\s*(\d+):(\d{2}):(\d{2})[,.](\d{1,3})`)
	reFPSLine  = regexp.MustCompile(`^\s*(\d{2}(?:[.,]\d+)?)\s*$`)
)

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// Detect guesses the subtitle format from the first lines that match a known pattern.
func Detect(text string) string {
	for i, line := range splitLines(text) {
		if i > 50 {
			break
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case reMicroDVD.MatchString(line):
			return MicroDVD
		case reMPL2.MatchString(line):
			return MPL2
		case reSRTTime.MatchString(line):
			return SubRip
		case reTMPlayer.MatchString(line):
			return TMPlayer
		}
	}
	return Unknown
}

// DeclaredFPS returns the frame rate some MicroDVD files declare in their
// first cue ("{1}{1}23.976"), or 0.
func DeclaredFPS(text string) float64 {
	for _, line := range splitLines(text) {
		m := reMicroDVD.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if f := reFPSLine.FindStringSubmatch(m[3]); f != nil && (m[1] == "0" || m[1] == "1") {
			v, _ := strconv.ParseFloat(strings.Replace(f[1], ",", ".", 1), 64)
			if v >= 10 && v <= 120 {
				return v
			}
		}
		return 0
	}
	return 0
}

// Parse reads subtitles of the given format. fps is only used for MicroDVD.
func Parse(text, format string, fps float64) ([]Cue, error) {
	var cues []Cue
	var err error
	switch format {
	case MicroDVD:
		if fps <= 0 {
			return nil, fmt.Errorf("MicroDVD needs a frame rate")
		}
		cues = parseFramed(text, reMicroDVD, func(n int64) time.Duration {
			return time.Duration(float64(n) / fps * float64(time.Second))
		}, true)
	case MPL2:
		cues = parseFramed(text, reMPL2, func(n int64) time.Duration {
			return time.Duration(n) * 100 * time.Millisecond
		}, false)
	case TMPlayer:
		cues = parseTMPlayer(text)
	case SubRip:
		cues, err = parseSRT(text)
	default:
		return nil, fmt.Errorf("unsupported subtitle format")
	}
	if err != nil {
		return nil, err
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no subtitles parsed")
	}
	fixTimes(cues)
	return cues, nil
}

func parseFramed(text string, re *regexp.Regexp, unit func(int64) time.Duration, microdvd bool) []Cue {
	var cues []Cue
	for _, line := range splitLines(text) {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if microdvd && (m[1] == "0" || m[1] == "1") && reFPSLine.MatchString(m[3]) {
			continue // frame-rate declaration, not a cue
		}
		start, _ := strconv.ParseInt(m[1], 10, 64)
		c := Cue{Start: unit(start), End: -1}
		if m[2] != "" {
			end, _ := strconv.ParseInt(m[2], 10, 64)
			c.End = unit(end)
		}
		c.Text = convertTags(m[3], microdvd)
		if strings.TrimSpace(c.Text) != "" {
			cues = append(cues, c)
		}
	}
	return cues
}

func parseTMPlayer(text string) []Cue {
	var cues []Cue
	for _, line := range splitLines(text) {
		m := reTMPlayer.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		start := time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(s)*time.Second
		t := convertTags(m[4], false)
		if strings.TrimSpace(t) == "" {
			continue
		}
		cues = append(cues, Cue{Start: start, End: -1, Text: t})
	}
	return cues
}

func parseSRT(text string) ([]Cue, error) {
	var cues []Cue
	var cur *Cue
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimRight(cur.Text, "\n")
			if cur.Text != "" {
				cues = append(cues, *cur)
			}
			cur = nil
		}
	}
	for _, line := range splitLines(text) {
		trimmed := strings.TrimSpace(line)
		if m := reSRTTime.FindStringSubmatch(trimmed); m != nil {
			flush()
			cur = &Cue{Start: srtTime(m[1:5]), End: srtTime(m[5:9])}
			continue
		}
		if cur == nil {
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		cur.Text += trimmed + "\n"
	}
	flush()
	return cues, nil
}

func srtTime(p []string) time.Duration {
	h, _ := strconv.Atoi(p[0])
	m, _ := strconv.Atoi(p[1])
	s, _ := strconv.Atoi(p[2])
	ms, _ := strconv.Atoi((p[3] + "00")[:3])
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(s)*time.Second + time.Duration(ms)*time.Millisecond
}

var reMicroDVDTag = regexp.MustCompile(`\{([yYcCfFsSpPhH]):[^}]*\}`)

// convertTags turns "|"-separated lines with MicroDVD ({y:i}) or MPL2-style
// ("/" prefix) italics into newline-separated text with HTML-like tags.
func convertTags(s string, microdvd bool) string {
	lines := strings.Split(s, "|")
	var global []string
	if microdvd {
		// Upper-case control codes apply to every line of the cue.
		for _, m := range reMicroDVDTag.FindAllStringSubmatch(lines[0], -1) {
			if m[1] == "Y" {
				global = styleTags(m[0])
			}
		}
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		var styles []string
		if microdvd {
			for _, m := range reMicroDVDTag.FindAllStringSubmatch(l, -1) {
				if m[1] == "y" {
					styles = append(styles, styleTags(m[0])...)
				}
			}
			l = reMicroDVDTag.ReplaceAllString(l, "")
		}
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "/") {
			l = strings.TrimSpace(l[1:])
			styles = append(styles, "i")
		}
		styles = dedupe(append(append([]string{}, global...), styles...))
		for i := len(styles) - 1; i >= 0; i-- {
			l = "<" + styles[i] + ">" + l + "</" + styles[i] + ">"
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func styleTags(tag string) []string {
	var s []string
	val := strings.ToLower(tag)
	for _, k := range []string{"i", "b", "u"} {
		if strings.Contains(val[3:], k) {
			s = append(s, k)
		}
	}
	return s
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := s[:0]
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// fixTimes fills in missing or inverted end times: such a cue lasts a
// length-based reading time, cut short where the next cue starts. Every cue
// is shown for at least half a second.
func fixTimes(cues []Cue) {
	for i := range cues {
		c := &cues[i]
		maxDur := readingTime(c.Text)
		if c.End <= c.Start {
			c.End = c.Start + maxDur
			if i+1 < len(cues) && cues[i+1].Start > c.Start && cues[i+1].Start < c.End {
				c.End = cues[i+1].Start - time.Millisecond
			}
		}
		if c.End-c.Start < 500*time.Millisecond {
			c.End = c.Start + 500*time.Millisecond
		}
	}
}

var reTag = regexp.MustCompile(`</?[ibu]>`)

func readingTime(text string) time.Duration {
	visible := len([]rune(reTag.ReplaceAllString(text, "")))
	d := time.Second + time.Duration(visible)*60*time.Millisecond
	if d > 6*time.Second {
		d = 6 * time.Second
	}
	if d < 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

// WriteSRT renders cues as SubRip text with CRLF line endings.
func WriteSRT(cues []Cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\r\n%s --> %s\r\n%s\r\n\r\n", i+1, srtStamp(c.Start), srtStamp(c.End),
			strings.ReplaceAll(c.Text, "\n", "\r\n"))
	}
	return b.String()
}

func srtStamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
