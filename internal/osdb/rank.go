package osdb

import (
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var (
	reTokens  = regexp.MustCompile(`[a-z0-9]+`)
	tokenKind = map[string]string{
		"2160p": "res", "1080p": "res", "1080i": "res", "720p": "res", "576p": "res", "480p": "res",
		"bluray": "src", "bdrip": "src", "brrip": "src", "bdremux": "src", "remux": "src", "web": "src",
		"webrip": "src", "webdl": "src", "hdtv": "src", "dvdrip": "src", "dvd": "src", "hdrip": "src",
		"x264": "codec", "h264": "codec", "avc": "codec", "x265": "codec", "h265": "codec",
		"hevc": "codec", "xvid": "codec", "divx": "codec",
	}
	// Sources that come from the same master usually share timing.
	srcFamily = map[string]string{"bluray": "bd", "bdrip": "bd", "brrip": "bd", "bdremux": "bd", "remux": "bd",
		"web": "web", "webrip": "web", "webdl": "web", "dvdrip": "dvd", "dvd": "dvd"}
)

func tokens(name string) []string {
	name = strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	name = strings.ReplaceAll(name, "web-dl", "webdl")
	name = strings.ReplaceAll(name, "blu-ray", "bluray")
	return reTokens.FindAllString(name, -1)
}

// releaseScore rates how likely a subtitle's release name matches the video
// file name: same source family, resolution, codec and release group count
// most, then overall word overlap.
func releaseScore(videoName, release string) float64 {
	vt, rt := tokens(videoName), tokens(release)
	if len(vt) == 0 || len(rt) == 0 {
		return 0
	}
	vs := map[string]bool{}
	for _, t := range vt {
		vs[t] = true
	}
	score := 0.0
	inter := 0
	seen := map[string]bool{}
	for _, t := range rt {
		if seen[t] {
			continue
		}
		seen[t] = true
		if vs[t] {
			inter++
			switch tokenKind[t] {
			case "src":
				score += 2
			case "res", "codec":
				score += 1
			}
		}
	}
	// Same source family (BluRay vs BDRip) without an exact token match.
	vf, rf := "", ""
	for _, t := range vt {
		if f, ok := srcFamily[t]; ok {
			vf = f
		}
	}
	for _, t := range rt {
		if f, ok := srcFamily[t]; ok {
			rf = f
		}
	}
	if vf != "" && vf == rf {
		score += 1
	}
	// Release group is conventionally the last word.
	if g := vt[len(vt)-1]; tokenKind[g] == "" && len(g) > 2 && g == rt[len(rt)-1] {
		score += 3
	}
	union := len(vs) + len(seen) - inter
	return score + 4*float64(inter)/float64(union)
}

// Rank orders candidates for a video, best first, dropping ones that cannot
// be used: foreign-parts-only, multi-file (CD1/CD2) and fileless results.
// Languages earlier in langs win over later ones, then human translations
// over machine ones, then the release-name, frame-rate and popularity score.
func Rank(cands []Subtitle, videoName string, videoFPS float64, langs []string) []Subtitle {
	langRank := map[string]int{}
	for i, l := range langs {
		langRank[strings.ToLower(l)] = i
	}
	type scored struct {
		s       Subtitle
		lang    int
		machine bool
		score   float64
	}
	var out []scored
	for _, s := range cands {
		lr, ok := langRank[strings.ToLower(s.Language)]
		if !ok || s.ForeignPartsOnly || len(s.Files) != 1 {
			continue
		}
		sc := releaseScore(videoName, s.Release)
		if videoFPS > 0 && s.FPS > 0 {
			if math.Abs(videoFPS-s.FPS) < 0.01 {
				sc += 2
			} else {
				sc -= 1 // alass corrects frame rates, but it is one more thing to get wrong
			}
		}
		if s.MovieHashMatch {
			sc += 20
		}
		if s.HearingImpaired {
			sc -= 0.5
		}
		if s.FromTrusted {
			sc += 0.5
		}
		sc += math.Log10(1+float64(s.DownloadCount)) / 2
		out = append(out, scored{s, lr, s.AITranslated || s.MachineTranslated, sc})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].lang != out[j].lang {
			return out[i].lang < out[j].lang
		}
		if out[i].machine != out[j].machine {
			return !out[i].machine // human translations first
		}
		return out[i].score > out[j].score
	})
	res := make([]Subtitle, len(out))
	for i, o := range out {
		res[i] = o.s
	}
	return res
}

var (
	reParenYear = regexp.MustCompile(`^(.*?)\s*[(\[]((?:19|20)\d{2})[)\]]`)
	reYear      = regexp.MustCompile(`[\s._\-]((?:19|20)\d{2})(?:[\s._\-]|$)`)
	reJunk      = regexp.MustCompile(`(?i)[\s._\-(\[]+(2160p|1080p|720p|480p|bluray|bdrip|brrip|web|webrip|web-dl|hdtv|dvdrip|x264|x265|hevc|h264)(?:[\s._\-)\]]|$).*$`)
)

// Guess is what a video's file name says about it.
type Guess struct {
	Title           string
	Year            int
	Season, Episode int // set for "Show.Name.S01E04" style names
}

var reSxxEyy = regexp.MustCompile(`(?i)^(.*?)[\s._\-]*\bS(\d{1,2})E(\d{1,3})\b`)

// GuessTitle extracts a search title from a video file name, e.g.
// "4 Months, 3 Weeks and 2 Days (2007) (1080p x265).mkv" -> "4 Months, 3 Weeks and 2 Days", 2007
// "All.Her.Fault.S01E04.1080p.WEB.H264-SYLiX.mkv" -> "All Her Fault", season 1, episode 4.
func GuessTitle(path string) Guess {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var g Guess
	atoi := func(s string) int {
		n := 0
		for _, c := range s {
			n = n*10 + int(c-'0')
		}
		return n
	}
	if m := reSxxEyy.FindStringSubmatch(name); m != nil {
		name, g.Season, g.Episode = m[1], atoi(m[2]), atoi(m[3])
		// "Show (2025) S01E04" or "Show.2025.S01E04"
		if y := reParenYear.FindStringSubmatch(name); y != nil && strings.TrimSpace(y[1]) != "" {
			name, g.Year = y[1], atoi(y[2])
		} else if all := reYear.FindAllStringSubmatchIndex(name+".", -1); len(all) > 0 && all[len(all)-1][0] > 0 {
			last := all[len(all)-1]
			name, g.Year = name[:last[0]], atoi(name[last[2]:last[3]])
		}
	} else if m := reParenYear.FindStringSubmatch(name); m != nil && strings.TrimSpace(m[1]) != "" {
		name, g.Year = m[1], atoi(m[2])
	} else if all := reYear.FindAllStringSubmatchIndex(name, -1); len(all) > 0 && all[len(all)-1][0] > 0 {
		// The last year-like number is the year: "Blade.Runner.2049.2017.1080p".
		last := all[len(all)-1]
		name, g.Year = name[:last[0]], atoi(name[last[2]:last[3]])
	} else {
		name = reJunk.ReplaceAllString(name, "")
	}
	name = strings.NewReplacer(".", " ", "_", " ").Replace(name)
	g.Title = strings.Join(strings.Fields(name), " ")
	return g
}

// NormTitle reduces a title for comparison: lower case, accents removed,
// punctuation dropped ("À nos amours!" -> "a nos amours").
func NormTitle(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, strings.ToLower(s))
	if err != nil {
		folded = strings.ToLower(s)
	}
	folded = strings.NewReplacer("ł", "l", "ø", "o", "æ", "ae", "ß", "ss", "&", " and ").Replace(folded)
	return strings.Join(reTokens.FindAllString(folded, -1), " ")
}

// TitleMatches reports whether any of names is the same title as title.
func TitleMatches(title string, names ...string) bool {
	want := NormTitle(title)
	if want == "" {
		return false
	}
	for _, n := range names {
		if NormTitle(n) == want {
			return true
		}
	}
	return false
}

// SameTitle keeps results whose movie (or, for episodes, series) title
// matches title, ignoring case, accents and punctuation.
func SameTitle(cands []Subtitle, title string) []Subtitle {
	var out []Subtitle
	for _, s := range cands {
		if TitleMatches(title, s.Title, s.ParentTitle) {
			out = append(out, s)
		}
	}
	return out
}
