// Command mkicon draws the Napi "Sync" mark and writes assets/napi.ico plus
// PNG previews. The mark is only rounded rectangles, so it is rendered here
// directly (4×4 supersampled) instead of needing an SVG rasteriser. Small
// sizes use their own simplified drawings, as in the logo proposal.
//
//	go run ./tools/mkicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

type rrect struct {
	x, y, w, h, r float64 // in the drawing's own units
	c             color.NRGBA
}

var (
	tile   = color.NRGBA{0x1D, 0x27, 0x40, 0xFF}
	blue   = color.NRGBA{0x4C, 0x8D, 0xFF, 0xFF}
	yellow = color.NRGBA{0xFF, 0xC2, 0x3D, 0xFF}
)

// drawing is a set of shapes on a square canvas of the given unit size.
type drawing struct {
	units  float64
	shapes []rrect
}

// full is the mark from the proposal (240 units): eight waveform bars, the
// playhead and the subtitle bar. Used from 48 px up.
var full = drawing{240, []rrect{
	{0, 0, 240, 240, 56, tile},
	{44, 80, 12, 24, 6, blue}, {64, 64, 12, 56, 6, blue}, {84, 48, 12, 88, 6, blue}, {104, 72, 12, 40, 6, blue},
	{124, 56, 12, 72, 6, blue}, {144, 40, 12, 104, 6, blue}, {164, 68, 12, 48, 6, blue}, {184, 78, 12, 28, 6, blue},
	{117, 30, 6, 182, 3, yellow},
	{64, 166, 112, 24, 12, yellow},
}}

// medium is drawn on a 32 px grid: four bars and the subtitle bar, no
// playhead. Used for 24–40 px.
var medium = drawing{32, []rrect{
	{0, 0, 32, 32, 7, tile},
	{7, 8, 3, 6, 1.5, blue}, {12, 4, 3, 14, 1.5, blue}, {17, 3, 3, 16, 1.5, blue}, {22, 7, 3, 8, 1.5, blue},
	{7, 23, 18, 4, 2, yellow},
}}

// small is drawn on the 16 px pixel grid: three bars and the subtitle bar.
// Used for 16 and 20 px.
var small = drawing{16, []rrect{
	{0, 0, 16, 16, 3.5, tile},
	{3, 5, 2, 4, 1, blue}, {7, 2, 2, 9, 1, blue}, {11, 5, 2, 4, 1, blue},
	{3, 12, 10, 2, 1, yellow},
}}

func drawingFor(size int) drawing {
	switch {
	case size <= 20:
		return small
	case size <= 40:
		return medium
	}
	return full
}

// inside reports whether point (px, py) lies in the rounded rectangle.
func (s rrect) inside(px, py float64) bool {
	cx, cy := s.x+s.w/2, s.y+s.h/2
	qx := math.Abs(px-cx) - (s.w/2 - s.r)
	qy := math.Abs(py-cy) - (s.h/2 - s.r)
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy)+math.Min(math.Max(qx, qy), 0) <= s.r
}

func render(d drawing, size int) *image.NRGBA {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := d.units / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64 // premultiplied accumulation
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/ss) * scale
					py := (float64(y) + (float64(sy)+0.5)/ss) * scale
					var cr, cg, cb, ca float64
					for _, s := range d.shapes { // painter's order, opaque shapes
						if s.inside(px, py) {
							cr, cg, cb, ca = float64(s.c.R), float64(s.c.G), float64(s.c.B), 255
						}
					}
					r, g, b, a = r+cr*ca/255, g+cg*ca/255, b+cb*ca/255, a+ca
				}
			}
			n := float64(ss * ss)
			a /= n
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				uint8(math.Round(r / n * 255 / a)), uint8(math.Round(g / n * 255 / a)),
				uint8(math.Round(b / n * 255 / a)), uint8(math.Round(a)),
			})
		}
	}
	return img
}

// writeICO packs PNG images into an .ico file (PNG entries, Vista and later).
func writeICO(path string, sizes []int, pngs [][]byte) error {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for i, p := range pngs {
		dim := uint8(sizes[i])
		if sizes[i] >= 256 {
			dim = 0
		}
		binary.Write(&buf, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(p)), uint32(offset)})
		offset += len(p)
	}
	for _, p := range pngs {
		buf.Write(p)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func main() {
	out := "assets"
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	var pngs [][]byte
	for _, size := range sizes {
		var b bytes.Buffer
		if err := png.Encode(&b, render(drawingFor(size), size)); err != nil {
			log.Fatal(err)
		}
		pngs = append(pngs, b.Bytes())
		if size == 16 || size == 32 || size == 256 {
			if err := os.WriteFile(filepath.Join(out, "icon-"+itoa(size)+".png"), b.Bytes(), 0o644); err != nil {
				log.Fatal(err)
			}
		}
	}
	if err := writeICO(filepath.Join(out, "napi.ico"), sizes, pngs); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s/napi.ico (%v px) and PNG previews", out, sizes)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
