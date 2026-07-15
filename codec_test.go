// =============================================================================
// codec_test.go — Tests for format detection and alpha analysis.
// =============================================================================

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func TestDetectFormat(t *testing.T) {
	png1 := makePNG(t, color.NRGBA{A: 255})
	if got := detectFormat(png1); got != fmtPNG {
		t.Errorf("png detect = %q, want png", got)
	}

	// JPEG magic bytes.
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 20)...)
	if got := detectFormat(jpg); got != fmtJPEG {
		t.Errorf("jpeg detect = %q, want jpeg", got)
	}

	// GIF.
	if got := detectFormat([]byte("GIF89a" + string(make([]byte, 20)))); got != fmtGIF {
		t.Errorf("gif detect = %q, want gif", got)
	}

	// EMF: header record type 1 + " EMF" signature at offset 40.
	emf := make([]byte, 44)
	emf[0] = 0x01
	copy(emf[40:44], " EMF")
	if got := detectFormat(emf); got != fmtEMF {
		t.Errorf("emf detect = %q, want emf", got)
	}

	// SVG text.
	svg := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	if got := detectFormat(svg); got != fmtSVG {
		t.Errorf("svg detect = %q, want svg", got)
	}

	// Unknown.
	if got := detectFormat([]byte("just some random text here!!")); got != fmtUnknown {
		t.Errorf("unknown detect = %q, want unknown", got)
	}
}

func TestImageHasAlpha(t *testing.T) {
	// Fully opaque RGBA image → no real alpha (JPEG-conversion candidate).
	opaque := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			opaque.Set(x, y, color.NRGBA{R: 100, G: 100, B: 100, A: 255})
		}
	}
	if imageHasAlpha(opaque) {
		t.Error("fully opaque RGBA reported as having alpha")
	}

	// One semi-transparent pixel → has alpha.
	trans := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			trans.Set(x, y, color.NRGBA{A: 255})
		}
	}
	trans.Set(2, 2, color.NRGBA{R: 0, G: 0, B: 0, A: 128})
	if !imageHasAlpha(trans) {
		t.Error("image with a transparent pixel reported as opaque")
	}
}

func TestIsAnimatedGIF(t *testing.T) {
	// Single-frame GIF.
	single := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var sbuf bytes.Buffer
	gif.Encode(&sbuf, single, nil)
	if isAnimatedGIF(sbuf.Bytes()) {
		t.Error("single-frame gif reported as animated")
	}

	// Multi-frame GIF.
	multi := &gif.GIF{
		Image: []*image.Paletted{
			image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}),
			image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}),
		},
		Delay: []int{10, 10},
	}
	var mbuf bytes.Buffer
	if err := gif.EncodeAll(&mbuf, multi); err != nil {
		t.Fatalf("encode multi gif: %v", err)
	}
	if !isAnimatedGIF(mbuf.Bytes()) {
		t.Error("multi-frame gif reported as single-frame")
	}
}

func TestThumbnailBase64(t *testing.T) {
	// A 400×200 PNG should thumbnail down without error and stay non-empty.
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	b64, err := thumbnailBase64(buf.Bytes(), 160)
	if err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if b64 == "" {
		t.Fatal("empty thumbnail")
	}
}

func TestAnalyzeMediaOnFixture(t *testing.T) {
	p, err := OpenPptx(syntheticPptx(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := p.BuildRelsIndex(); err != nil {
		t.Fatalf("rels: %v", err)
	}
	media := AnalyzeMedia(p, CompressionOptions{Preset: "balanced", MinSizeKB: 0})
	if len(media) != 1 {
		t.Fatalf("expected 1 media part, got %d", len(media))
	}
	m := media[0]
	if m.Format != fmtPNG {
		t.Errorf("format = %q, want png", m.Format)
	}
	if m.Width != 1 || m.Height != 1 {
		t.Errorf("dimensions = %dx%d, want 1x1", m.Width, m.Height)
	}
	if m.RefCount != 1 {
		t.Errorf("refCount = %d, want 1", m.RefCount)
	}
	if m.HasAlpha {
		t.Error("opaque fixture png reported as having alpha")
	}
}
