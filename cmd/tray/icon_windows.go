//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"

	mediaforge "github.com/braydin72/mediaforge"
)

// loadIcon reads logo.png from the embedded web templates, resizes it to 64×64,
// and wraps it in a Vista+ ICO container for systray.SetIcon.
func loadIcon() ([]byte, error) {
	data, err := mediaforge.WebFS.ReadFile("web/templates/logo.png")
	if err != nil {
		return nil, err
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	small := resizeNN(src, 64, 64)
	var buf bytes.Buffer
	if err := png.Encode(&buf, small); err != nil {
		return nil, err
	}
	return makeICO(buf.Bytes(), 64, 64), nil
}

// resizeNN resizes src to w×h using nearest-neighbor interpolation.
func resizeNN(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, src.At(
				sb.Min.X+x*sw/w,
				sb.Min.Y+y*sh/h,
			))
		}
	}
	return dst
}

// makeICO wraps pngData in a single-entry Vista+ ICO container.
// Modern Windows (Vista+) supports PNG data embedded directly in ICO files.
func makeICO(pngData []byte, w, h int) []byte {
	// ICONDIR: reserved(2) + type(2=icon) + count(2)
	out := []byte{0, 0, 1, 0, 1, 0}
	// ICONDIRENTRY: width(1) + height(1) + colorCount(1) + reserved(1) + planes(2) + bitCount(2)
	wb := byte(w)
	if w >= 256 {
		wb = 0
	}
	hb := byte(h)
	if h >= 256 {
		hb = 0
	}
	out = append(out, wb, hb, 0, 0, 1, 0, 32, 0)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(pngData)))
	out = binary.LittleEndian.AppendUint32(out, 22) // offset = 6 (ICONDIR) + 16 (ICONDIRENTRY)
	return append(out, pngData...)
}
