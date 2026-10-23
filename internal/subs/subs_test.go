package subs

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
)

const sample = "Zażółć gęślą jaźń. Śmiało, Łukasz!"

func TestDecode(t *testing.T) {
	cp, _ := charmap.Windows1250.NewEncoder().String(sample)
	iso, _ := charmap.ISO8859_2.NewEncoder().String(sample)
	for _, tc := range []struct {
		in, charset string
	}{
		{sample, "utf-8"},
		{"\xEF\xBB\xBF" + sample, "utf-8"},
		{cp, "windows-1250"},
		{iso, "iso-8859-2"},
	} {
		got, cs := Decode([]byte(tc.in))
		if got != sample || cs != tc.charset {
			t.Errorf("Decode(%q) = %q, %s; want %s", tc.in, got, cs, tc.charset)
		}
	}
}

func TestEncode(t *testing.T) {
	if got := Encode("ą", EncUTF8BOM); string(got) != "\xEF\xBB\xBFą" {
		t.Errorf("utf8bom: %q", got)
	}
	if got := Encode("ąś€😀", EncCP1250); string(got) != "\xB9\x9C\x80?" {
		t.Errorf("cp1250: %q", got)
	}
}

func TestDetect(t *testing.T) {
	for in, want := range map[string]string{
		"{1}{1}23.976\r\n{100}{200}Hej":           MicroDVD,
		"[10][20]Hej|/druga":                      MPL2,
		"00:00:01:Hej\n00:00:05=Dwa":              TMPlayer,
		"1\n00:00:01,000 --> 00:00:02,500\nHej\n": SubRip,
		"zwykły tekst":                            Unknown,
	} {
		if got := Detect(in); got != want {
			t.Errorf("Detect(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMicroDVD(t *testing.T) {
	text := "{1}{1}25\r\n{25}{50}{y:i}Pierwsza|druga\r\n{75}{}{Y:b}Trzecia|czwarta\r\n{150}{175}/kursywa|{c:$0000ff}zwykła\r\n"
	if fps := DeclaredFPS(text); fps != 25 {
		t.Fatalf("DeclaredFPS = %v", fps)
	}
	cues, err := Parse(text, MicroDVD, 25)
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{
		{time.Second, 2 * time.Second, "<i>Pierwsza</i>\ndruga"},
		{3 * time.Second, 5 * time.Second, "<b>Trzecia</b>\n<b>czwarta</b>"},
		{6 * time.Second, 7 * time.Second, "<i>kursywa</i>\nzwykła"},
	}
	if len(cues) != len(want) {
		t.Fatalf("got %d cues: %+v", len(cues), cues)
	}
	for i := range want {
		if cues[i] != want[i] {
			t.Errorf("cue %d = %+v, want %+v", i, cues[i], want[i])
		}
	}
}

func TestMPL2AndTMPlayer(t *testing.T) {
	cues, err := Parse("[10][25]Raz|/Dwa\n", MPL2, 0)
	if err != nil || cues[0].Start != time.Second || cues[0].End != 2500*time.Millisecond || cues[0].Text != "Raz\n<i>Dwa</i>" {
		t.Fatalf("mpl2: %+v %v", cues, err)
	}
	cues, err = Parse("00:00:01:Raz\n00:00:02:Dwa|Trzy\n", TMPlayer, 0)
	if err != nil || len(cues) != 2 || cues[0].End != 2*time.Second-time.Millisecond || cues[1].Text != "Dwa\nTrzy" {
		t.Fatalf("tmplayer: %+v %v", cues, err)
	}
}

func TestSRTRoundTrip(t *testing.T) {
	in := "1\r\n00:00:01,000 --> 00:00:02,500\r\nRaz\r\nDwa\r\n\r\n2\r\n00:01:00,100 --> 00:01:02,000\r\nTrzy\r\n\r\n"
	cues, err := Parse(in, SubRip, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out := WriteSRT(cues); out != in {
		t.Fatalf("round trip:\n%q\n%q", out, in)
	}
}

func TestMicroDVDNeedsFPS(t *testing.T) {
	if _, err := Parse("{1}{2}x", MicroDVD, 0); err == nil || !strings.Contains(err.Error(), "frame rate") {
		t.Fatalf("got %v", err)
	}
}

func TestDetectLanguage(t *testing.T) {
	pl := strings.Repeat("1\n00:00:01,000 --> 00:00:02,000\nNie wiem, czy to jest dobry pomysł.\n- Już się zgodziłeś!\n\n", 10)
	en := strings.Repeat("1\n00:00:01,000 --> 00:00:02,000\nI don't know what you want from me.\n- Is that the plan?\n\n", 10)
	for in, want := range map[string]string{pl: "PL", en: "ENG", "1\n00:00:01,000 --> 00:00:02,000\n...\n": ""} {
		if got := DetectLanguage(in); got != want {
			t.Errorf("DetectLanguage(%.40q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrokenEndTimes(t *testing.T) {
	// From a real file: two cues whose end time was mistyped.
	in := "1\r\n00:44:50,982 --> 01:29:34,999\r\nPierwsza\r\n\r\n2\r\n00:44:52,017 --> 00:44:53,018\r\nDruga\r\n\r\n" +
		"3\r\n01:31:52,012 --> 02:53:54,017\r\nOstatnia\r\n\r\n"
	cues, err := Parse(in, SubRip, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cues[0].End != 44*time.Minute+52*time.Second+16*time.Millisecond {
		t.Errorf("first cue should end before the second starts, got %v", cues[0].End)
	}
	if d := cues[2].End - cues[2].Start; d > 6*time.Second {
		t.Errorf("last cue lasts %v", d)
	}
}
