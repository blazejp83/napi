package media

import (
	"math"
	"testing"
	"time"
)

func TestFrameRate(t *testing.T) {
	for _, tc := range []struct {
		file, src string
		want      float64
	}{
		{"testdata/fps_25.mkv", "mkv", 25},
		{"testdata/fps_24000_1001.mkv", "mkv", 24000.0 / 1001},
		{"testdata/fps_25.mp4", "mp4", 25},
		{"testdata/fps_24000_1001.mp4", "mp4", 24000.0 / 1001},
		{"testdata/fps_30000_1001.avi", "avi", 30000.0 / 1001},
	} {
		// Hide ffprobe so the container parsers are what is tested.
		t.Setenv("PATH", "")
		got, src, err := FrameRate(tc.file)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		if math.Abs(got-tc.want) > 1e-9 || src != tc.src {
			t.Errorf("%s: got %.6f from %s, want %.6f from %s", tc.file, got, src, tc.want, tc.src)
		}
	}
}

func TestFrameRateUnknown(t *testing.T) {
	t.Setenv("PATH", "")
	if _, _, err := FrameRate("fps_test.go"); err == nil {
		t.Fatal("expected an error for a non-video file")
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("PATH", "")
	for _, f := range []string{"testdata/fps_25.mkv", "testdata/fps_24000_1001.mp4", "testdata/fps_30000_1001.avi"} {
		d, err := Duration(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		// The fixtures are 0.5 s clips.
		if d < 450*time.Millisecond || d > 600*time.Millisecond {
			t.Errorf("%s: duration %v", f, d)
		}
	}
}
