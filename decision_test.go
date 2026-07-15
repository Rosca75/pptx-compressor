// =============================================================================
// decision_test.go — Tests for the decision matrix and the encoders.
// =============================================================================

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// solidRGBA builds an opaque w×h image filled with c (RGBA, no transparency).
func solidRGBA(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// gradientAlpha builds a w×h image with a horizontal alpha gradient (real
// transparency) so quantization must preserve alpha.
func gradientAlpha(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: uint8(x * 255 / w)})
		}
	}
	return img
}

func TestDecideOpaquePngToJpeg(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/i.png", Format: fmtPNG, Width: 800, Height: 600, Bytes: 500 * 1024, HasAlpha: false}
	a := DecideAction(m, CompressionOptions{Preset: "balanced"})
	if a.Kind != actPngToJpeg || a.NewExt != "jpeg" {
		t.Fatalf("opaque png → got %+v, want png->jpeg", a)
	}
}

func TestDecideTransparentPngStaysPng(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/i.png", Format: fmtPNG, Width: 800, Height: 600, Bytes: 500 * 1024, HasAlpha: true}
	// Balanced does NOT quantize → lossless PNG re-encode.
	a := DecideAction(m, CompressionOptions{Preset: "balanced"})
	if a.Kind != actRecompressPng || a.NewExt != "png" {
		t.Fatalf("transparent png (balanced) → got %+v, want recompress-png", a)
	}
	// Aggressive quantizes.
	a2 := DecideAction(m, CompressionOptions{Preset: "aggressive"})
	if a2.Kind != actQuantizePng || !a2.Quantize {
		t.Fatalf("transparent png (aggressive) → got %+v, want quantize-png", a2)
	}
}

func TestDecideTinyImageSkipped(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/i.png", Format: fmtPNG, Width: 10, Height: 10, Bytes: 2 * 1024, HasAlpha: false}
	a := DecideAction(m, CompressionOptions{Preset: "balanced", MinSizeKB: 10})
	if a.Kind != actSkip {
		t.Fatalf("tiny image → got %+v, want skip", a)
	}
}

func TestDecideVectorAndMediaSkipped(t *testing.T) {
	for _, f := range []string{fmtEMF, fmtWMF, fmtSVG, fmtMedia, fmtUnknown} {
		m := MediaInfo{PartName: "ppt/media/x", Format: f, Bytes: 1 << 20}
		if a := DecideAction(m, CompressionOptions{Preset: "balanced"}); a.Kind != actSkip {
			t.Errorf("format %q → got %+v, want skip", f, a)
		}
	}
}

func TestDecidePerImageOverride(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/i.png", Format: fmtPNG, Width: 800, Height: 600, Bytes: 500 * 1024}
	opts := CompressionOptions{Preset: "balanced", PerImageOverrides: map[string]string{
		"ppt/media/i.png": "skip",
	}}
	if a := DecideAction(m, opts); a.Kind != actSkip {
		t.Fatalf("override skip → got %+v", a)
	}
	opts.PerImageOverrides["ppt/media/i.png"] = "remove"
	if a := DecideAction(m, opts); a.Kind != actRemove {
		t.Fatalf("override remove → got %+v", a)
	}
}

func TestDecideWebpOverridesFormat(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/i.png", Format: fmtPNG, Width: 800, Height: 600, Bytes: 500 * 1024, HasAlpha: true}
	a := DecideAction(m, CompressionOptions{Preset: "balanced", UseWebp: true})
	if a.Kind != actWebp || a.NewExt != "webp" {
		t.Fatalf("webp opt-in → got %+v, want webp", a)
	}
}

// TestEncodeJPEG verifies the jpegli WASM encoder runs and produces decodable
// JPEG bytes in this environment.
func TestEncodeJPEG(t *testing.T) {
	img := solidRGBA(64, 64, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	data, err := encodeJPEG(img, 82)
	if err != nil {
		t.Fatalf("encodeJPEG: %v", err)
	}
	if detectFormat(data) != fmtJPEG {
		t.Fatalf("encodeJPEG did not produce JPEG bytes")
	}
	if _, _, err := decodeImage(data); err != nil {
		t.Fatalf("re-decode jpeg: %v", err)
	}
}

// TestEncodeWebP verifies the webp WASM encoder runs.
func TestEncodeWebP(t *testing.T) {
	img := solidRGBA(64, 64, color.NRGBA{R: 10, G: 220, B: 90, A: 255})
	data, err := encodeWebP(img, 82)
	if err != nil {
		t.Fatalf("encodeWebP: %v", err)
	}
	if detectFormat(data) != fmtWebP {
		t.Fatalf("encodeWebP did not produce WebP bytes, got %q", detectFormat(data))
	}
}

// TestQuantizePreservesAlpha checks that median-cut output keeps transparency
// (a tRNS chunk / alpha in the decoded palette).
func TestQuantizePreservesAlpha(t *testing.T) {
	src := gradientAlpha(64, 64)
	data, err := quantizePNG(src, 64)
	if err != nil {
		t.Fatalf("quantizePNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode quantized png: %v", err)
	}
	if !imageHasAlpha(img) {
		t.Fatal("quantized image lost its alpha channel")
	}
}

// TestFlattenOntoWhite ensures transparency is composited (not left black).
func TestFlattenOntoWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 0, G: 0, B: 0, A: 0}) // fully transparent
	flat := flattenOntoWhite(img)
	r, g, b, a := flat.At(0, 0).RGBA()
	if a != 0xffff {
		t.Fatalf("flattened pixel not opaque: a=%d", a)
	}
	// Transparent-over-white should be white.
	if r < 0xff00 || g < 0xff00 || b < 0xff00 {
		t.Fatalf("transparent area not composited to white: rgb=%d,%d,%d", r, g, b)
	}
}

// TestResizeNeverUpscales confirms small images are returned untouched.
func TestResizeNeverUpscales(t *testing.T) {
	img := solidRGBA(100, 50, color.NRGBA{A: 255})
	out, did := resizeToMaxEdge(img, 2000)
	if did {
		t.Fatal("resize upscaled a small image")
	}
	if out.Bounds().Dx() != 100 {
		t.Fatalf("resize changed dimensions: %v", out.Bounds())
	}
	// Downscale path.
	out2, did2 := resizeToMaxEdge(img, 50)
	if !did2 || out2.Bounds().Dx() != 50 {
		t.Fatalf("downscale to edge 50 failed: did=%v bounds=%v", did2, out2.Bounds())
	}
}
