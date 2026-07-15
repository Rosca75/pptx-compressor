// =============================================================================
// codec.go — Format detection, alpha analysis, and image encoders.
// =============================================================================
//
// This file centralises everything format-specific so the analyzer and the
// compression pipeline stay format-agnostic. It is split into two halves:
//
//   DETECTION (Phase 2):
//     - detectFormat:   identify the image type from magic bytes, never the
//                       file extension (extensions in .pptx are unreliable).
//     - decodeImage:    decode any supported raster into an image.Image.
//     - imageHasAlpha:  decide REAL transparency by sampling the alpha channel;
//                       metadata is not trusted (an RGBA PNG can be fully opaque).
//     - isAnimatedGIF:  multi-frame GIFs are skipped (never re-encoded).
//
//   ENCODING (Phase 3):
//     - encodeJPEG (jpegli), encodePNG, quantizePNG (median-cut), encodeWebP.
//
// jpegli (github.com/gen2brain/jpegli) and webp (github.com/gen2brain/webp) are
// CGo-free WASM encoders — the approved exception to the no-CGo build rule.
// Decoding of BMP/TIFF/WebP uses the pure-Go decoders from golang.org/x/image.
// =============================================================================

package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/gif"
	stdjpeg "image/jpeg" // stdlib JPEG encoder, used for lightweight thumbnails
	_ "image/png"        // register the PNG decoder

	_ "golang.org/x/image/bmp" // register BMP decoder
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff" // register TIFF decoder
	_ "golang.org/x/image/webp" // register WebP decoder (static only, fine for v1)
)

// Image format identifiers returned by detectFormat. These string values also
// surface in the UI (MediaInfo.Format), so keep them lower-case and stable.
const (
	fmtPNG     = "png"
	fmtJPEG    = "jpeg"
	fmtGIF     = "gif"
	fmtBMP     = "bmp"
	fmtTIFF    = "tiff"
	fmtWebP    = "webp"
	fmtEMF     = "emf"
	fmtWMF     = "wmf"
	fmtSVG     = "svg"
	fmtMedia   = "media"   // audio/video — reported, never processed in v1
	fmtUnknown = "unknown" // unrecognised bytes
)

// Action-kind labels. These describe what the pipeline intends to do with a
// part and are surfaced in the UI (MediaInfo.ProposedAction). Kept stable
// because the frontend and report text reference them.
const (
	actSkip           = "skip"            // leave the part exactly as-is
	actRemove         = "remove"          // neutralise to a 1×1 pixel (per-image override)
	actRecompressJPEG = "recompress-jpeg" // JPEG in → smaller JPEG out
	actPngToJpeg      = "png->jpeg"       // opaque PNG/BMP/TIFF/WebP → JPEG
	actRecompressPng  = "recompress-png"  // transparent PNG re-encoded losslessly
	actQuantizePng    = "quantize-png"    // transparent PNG → ≤256-color paletted PNG
	actWebp           = "webp"            // encode WebP (opt-in)
	actKept           = "kept"            // runtime: re-encode was not smaller, original kept
)

// =============================================================================
// detectFormat — identify an image by its leading bytes
// =============================================================================

// detectFormat inspects the magic bytes at the start of data and returns one of
// the fmt* constants. It deliberately ignores the file extension: a .pptx often
// stores images under a wrong or generic extension, and only the bytes are
// authoritative (see CLAUDE.md domain rules).
func detectFormat(data []byte) string {
	if len(data) < 12 {
		return fmtUnknown
	}

	switch {
	// PNG: 89 50 4E 47 0D 0A 1A 0A
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return fmtPNG

	// JPEG: FF D8 FF
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return fmtJPEG

	// GIF: "GIF87a" or "GIF89a"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return fmtGIF

	// BMP: "BM"
	case bytes.HasPrefix(data, []byte("BM")):
		return fmtBMP

	// TIFF: "II*\0" (little-endian) or "MM\0*" (big-endian)
	case bytes.HasPrefix(data, []byte{0x49, 0x49, 0x2A, 0x00}),
		bytes.HasPrefix(data, []byte{0x4D, 0x4D, 0x00, 0x2A}):
		return fmtTIFF

	// WebP: "RIFF" .... "WEBP" (the format tag sits at bytes 8-11).
	case bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return fmtWebP

	// EMF (Enhanced Metafile): record type 1 (EMR_HEADER) as a little-endian
	// uint32 at offset 0, plus the " EMF" signature at offset 40.
	case len(data) >= 44 &&
		data[0] == 0x01 && data[1] == 0x00 && data[2] == 0x00 && data[3] == 0x00 &&
		bytes.Equal(data[40:44], []byte(" EMF")):
		return fmtEMF

	// WMF (Windows Metafile): placeable header D7 CD C6 9A, or a standard header
	// (01 00 09 00 / 02 00 09 00).
	case bytes.HasPrefix(data, []byte{0xD7, 0xCD, 0xC6, 0x9A}),
		bytes.HasPrefix(data, []byte{0x01, 0x00, 0x09, 0x00}),
		bytes.HasPrefix(data, []byte{0x02, 0x00, 0x09, 0x00}):
		return fmtWMF
	}

	// SVG is XML text — sniff for an <svg root or an XML prolog followed by <svg.
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	lower := bytes.ToLower(head)
	if bytes.Contains(lower, []byte("<svg")) {
		return fmtSVG
	}

	// Audio/video containers frequently embedded in decks (best-effort sniff).
	// The "ftyp" box marks MP4/MOV; "OggS" marks Ogg; "ID3"/FF Fx marks MP3.
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return fmtMedia
	}
	if bytes.HasPrefix(data, []byte("OggS")) || bytes.HasPrefix(data, []byte("ID3")) {
		return fmtMedia
	}

	return fmtUnknown
}

// isRasterFormat reports whether fmt is a bitmap format we can decode and
// re-encode. Vectors (emf/wmf/svg) and media are excluded.
func isRasterFormat(f string) bool {
	switch f {
	case fmtPNG, fmtJPEG, fmtGIF, fmtBMP, fmtTIFF, fmtWebP:
		return true
	}
	return false
}

// =============================================================================
// decodeImage — decode any supported raster into an image.Image
// =============================================================================

// decodeImage decodes data using the standard image.Decode, which dispatches to
// whichever decoder recognises the magic bytes (all needed decoders are
// registered via the blank imports at the top of this file). It returns the
// decoded image and the format name reported by the decoder.
func decodeImage(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode image: %w", err)
	}
	return img, format, nil
}

// decodeConfig reads just the dimensions (and color model) without decoding the
// full pixel data — cheap, used during analysis.
func decodeConfig(data []byte) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	return cfg, err
}

// =============================================================================
// isAnimatedGIF — multi-frame detection
// =============================================================================

// isAnimatedGIF reports whether data is a GIF with more than one frame. Animated
// GIFs are always skipped: re-encoding to a single-frame format would drop the
// animation. A decode error is treated as "not animated" so the caller falls
// back to its normal skip/handle logic.
func isAnimatedGIF(data []byte) bool {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return false
	}
	return len(g.Image) > 1
}

// =============================================================================
// imageHasAlpha — real transparency by sampling the alpha channel
// =============================================================================

// alphaSampleBudget is the pixel-count threshold (4 megapixels) above which we
// sample every 4th pixel instead of every pixel. Below it we scan every pixel.
const alphaSampleBudget = 4 * 1000 * 1000

// imageHasAlpha decides whether an image ACTUALLY uses transparency by scanning
// its alpha channel. This is deliberate: a PNG or WebP can be encoded with an
// alpha channel yet be fully opaque, which is exactly the case we want to
// convert to JPEG. Metadata/format alone is never trusted.
//
// Sampling: for images up to alphaSampleBudget pixels every pixel is checked;
// for larger images every 4th pixel is checked (a stride that still reliably
// catches transparency while bounding the cost on huge images). Any alpha value
// below 255 (opaque) means the image has real transparency.
func imageHasAlpha(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return false
	}

	// Fast path: an opaque color model can never carry alpha.
	if opaqueModel(img) {
		return false
	}

	stride := 1
	if w*h > alphaSampleBudget {
		stride = 4
	}

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x += stride {
			// At() returns color.Color; Alpha via RGBA() is 16-bit (0..65535).
			// Fully opaque is 65535; anything less is real transparency.
			_, _, _, a := img.At(x, y).RGBA()
			if a < 0xffff {
				return true
			}
		}
	}
	return false
}

// opaqueModel returns true for color models that structurally cannot hold
// transparency, letting imageHasAlpha short-circuit without scanning pixels.
func opaqueModel(img image.Image) bool {
	switch img.(type) {
	case *image.YCbCr, *image.CMYK, *image.Gray, *image.Gray16:
		return true
	}
	return false
}

// =============================================================================
// thumbnailBase64 — small preview JPEG for the UI
// =============================================================================

// thumbnailBase64 decodes data, downscales it so its longest edge is at most
// maxEdge pixels (never upscaling), and returns a base64-encoded JPEG string
// (no "data:" prefix). Used by App.GetImagePreview. It intentionally uses the
// stdlib JPEG encoder — previews do not need jpegli's extra compression.
func thumbnailBase64(data []byte, maxEdge int) (string, error) {
	img, _, err := decodeImage(data)
	if err != nil {
		return "", err
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}

	// Downscale only when the image exceeds the thumbnail edge.
	if longest > maxEdge && longest > 0 {
		ratio := float64(maxEdge) / float64(longest)
		nw := int(float64(w) * ratio)
		nh := int(float64(h) * ratio)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		// CatmullRom is a high-quality resampling kernel.
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img = dst
	}

	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 80}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
