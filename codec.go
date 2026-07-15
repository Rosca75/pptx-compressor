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
	"image/color"
	stddraw "image/draw"
	"image/gif"
	stdjpeg "image/jpeg" // stdlib JPEG encoder, used for lightweight thumbnails
	"image/png"
	"sort"
	"strings"

	"github.com/gen2brain/jpegli"
	gowebp "github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp"  // register BMP decoder
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

	// Downscale to the thumbnail edge (never upscales).
	img, _ = resizeToMaxEdge(img, maxEdge)

	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 80}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// =============================================================================
// Encoders (Phase 3) — jpegli JPEG, stdlib PNG, quantized PNG, WebP
// =============================================================================

// encodeJPEG encodes img as a JPEG using jpegli at the given quality (1–100).
// jpegli produces standard JPEG bytes roughly 35% smaller than libjpeg at equal
// quality and is CGo-free (WASM/wazero), the sanctioned exception to the no-CGo
// rule. 4:2:0 chroma subsampling matches how photos are normally stored and
// gives the best size; JPEG cannot carry alpha, so callers must only pass
// opaque images here.
func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	var buf bytes.Buffer
	err := jpegli.Encode(&buf, img, &jpegli.EncodingOptions{
		Quality:           quality,
		ChromaSubsampling: image.YCbCrSubsampleRatio420,
	})
	if err != nil {
		return nil, fmt.Errorf("jpegli encode: %w", err)
	}
	return buf.Bytes(), nil
}

// encodePNG encodes img as a PNG at best compression using the stdlib encoder.
// Used for transparent images when quantization is off (lossless re-encode).
func encodePNG(img image.Image) ([]byte, error) {
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	var buf bytes.Buffer
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return buf.Bytes(), nil
}

// quantizePNG reduces img to a palette of at most maxColors colours (with alpha)
// via median-cut, then encodes it as a PNG at best compression. Go's PNG encoder
// writes the tRNS chunk automatically when the palette contains transparency, so
// the alpha channel survives. Used for genuinely transparent PNGs where a full
// 8-bit-per-channel image is wasteful (logos, flat graphics).
func quantizePNG(img image.Image, maxColors int) ([]byte, error) {
	if maxColors < 2 {
		maxColors = 2
	}
	if maxColors > 256 {
		maxColors = 256
	}
	paletted := medianCutQuantize(img, maxColors)
	return encodePNG(paletted)
}

// encodeWebP encodes img as WebP. When the image has alpha we still use lossy
// mode (WebP carries alpha losslessly alongside lossy colour), controlled by
// quality. Opt-in only — see the domain rules. gen2brain/webp is CGo-free.
func encodeWebP(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	var buf bytes.Buffer
	err := gowebp.Encode(&buf, img, gowebp.Options{
		Quality: quality,
		Method:  4, // balanced quality/speed
	})
	if err != nil {
		return nil, fmt.Errorf("webp encode: %w", err)
	}
	return buf.Bytes(), nil
}

// flattenOntoWhite returns an opaque RGBA copy of img with any transparency
// composited over a white background. Required before JPEG encoding, which has
// no alpha channel — without flattening, transparent areas would encode as
// black. Callers only convert to JPEG when alpha is absent, but flattening is
// cheap and makes the "no gain" fallback robust against stray alpha.
func flattenOntoWhite(img image.Image) image.Image {
	if _, ok := img.(*image.YCbCr); ok {
		return img // decoded JPEGs are already opaque YCbCr
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	// Paint a white background, then composite the source over it.
	stddraw.Draw(dst, b, image.NewUniform(color.White), image.Point{}, stddraw.Src)
	stddraw.Draw(dst, b, img, b.Min, stddraw.Over)
	return dst
}

// =============================================================================
// DecideAction — the decision matrix (BUILD.md §Phase 3)
// =============================================================================

// Action is the concrete plan the pipeline executes for a single media part.
// It captures the encoder to use, the target format (for renames), the quality,
// and whether to downscale/quantize first.
type Action struct {
	// Kind is one of the act* labels (drives UI text and reporting).
	Kind string

	// NewExt is the target file extension WITHOUT a dot (e.g. "jpeg", "webp").
	// Empty means the part keeps its current name (same-format recompress).
	NewExt string

	// ContentType is the MIME type declared for NewExt in [Content_Types].xml.
	ContentType string

	// Quality is the JPEG/WebP quality to encode at.
	Quality int

	// MaxEdge is the downscale cap (0 = no downscale).
	MaxEdge int

	// Quantize requests median-cut palette quantization (transparent PNG path).
	Quantize bool
}

// isNoOp reports whether the action means "leave the bytes exactly as they are".
func (a Action) isNoOp() bool { return a.Kind == actSkip || a.Kind == actKept }

// DecideAction applies the decision matrix from BUILD.md to a single media part
// and returns the plan. It does NOT encode anything — it only decides. The
// never-larger guarantee is enforced later, in the pipeline, after encoding.
//
// Matrix (first matching row wins):
//
//	bytes < minSize                         → Skip
//	per-image override                      → override (skip / remove)
//	EMF / WMF / SVG / media / unknown       → Skip (vector or non-raster)
//	animated GIF                            → Skip (handled by caller via bytes)
//	JPEG                                    → recompress JPEG (jpegli)
//	opaque PNG/BMP/TIFF/WebP + convertOpaque → PNG→JPEG (rename)
//	transparent PNG + quantize              → quantized paletted PNG
//	transparent PNG (quantize off)          → lossless PNG re-encode
//	useWebp (any raster)                    → WebP (overrides format choice)
func DecideAction(m MediaInfo, opts CompressionOptions) Action {
	opts = resolveOptions(opts)

	// Per-image override takes precedence over everything except: an override is
	// still meaningful below the size threshold.
	if ov, ok := opts.PerImageOverrides[m.PartName]; ok {
		switch strings.ToLower(ov) {
		case "skip":
			return Action{Kind: actSkip}
		case "remove":
			return Action{Kind: actRemove}
			// "auto"/"force" fall through to the normal decision.
		}
	}

	// Below the minimum size we never touch the image.
	if m.Bytes < int64(opts.MinSizeKB)*1024 {
		return Action{Kind: actSkip}
	}

	// Vectors, media and unrecognised bytes are never re-encoded.
	switch m.Format {
	case fmtEMF, fmtWMF, fmtSVG, fmtMedia, fmtUnknown:
		return Action{Kind: actSkip}
	}

	q := opts.JpegQuality
	edge := opts.MaxEdgePx

	// WebP opt-in overrides the format choice for any raster.
	if opts.UseWebp && isRasterFormat(m.Format) {
		return Action{
			Kind:        actWebp,
			NewExt:      "webp",
			ContentType: "image/webp",
			Quality:     q,
			MaxEdge:     edge,
		}
	}

	switch m.Format {
	case fmtJPEG:
		// Same format: recompress in place, no rename.
		return Action{Kind: actRecompressJPEG, Quality: q, MaxEdge: edge}

	case fmtGIF:
		// Single-frame GIF: re-encode down the PNG path (caller guarantees it is
		// not animated before reaching here).
		if m.HasAlpha && opts.QuantizeTransparentPng {
			return Action{Kind: actQuantizePng, NewExt: "png", ContentType: "image/png", MaxEdge: edge, Quantize: true}
		}
		return Action{Kind: actRecompressPng, NewExt: "png", ContentType: "image/png", MaxEdge: edge}

	case fmtPNG, fmtBMP, fmtTIFF, fmtWebP:
		if !m.HasAlpha && opts.ConvertOpaquePng {
			return Action{Kind: actPngToJpeg, NewExt: "jpeg", ContentType: "image/jpeg", Quality: q, MaxEdge: edge}
		}
		if m.HasAlpha && opts.QuantizeTransparentPng {
			return Action{Kind: actQuantizePng, NewExt: "png", ContentType: "image/png", MaxEdge: edge, Quantize: true}
		}
		return Action{Kind: actRecompressPng, NewExt: "png", ContentType: "image/png", MaxEdge: edge}
	}

	return Action{Kind: actSkip}
}

// =============================================================================
// medianCutQuantize — reduce an image to a ≤maxColors palette (with alpha)
// =============================================================================
//
// Median-cut is a classic colour-quantization algorithm. It treats every pixel
// as a point in 4-D RGBA space and repeatedly splits the most spread-out "box"
// of colours in half at its median along its longest axis, until we have as
// many boxes as we want palette entries. Each final box contributes one palette
// colour (the average of the colours it contains). The result is an
// image.Paletted; Go's PNG encoder emits a tRNS chunk automatically when the
// palette carries alpha, so transparency is preserved.
//
// We implement it here (rather than adding a dependency) as required by the
// build rules. It is ~self-contained and heavily commented for the same reason.

// rgba8 is a single pixel as four 8-bit non-premultiplied channels.
type rgba8 struct{ r, g, b, a uint8 }

// colorBox is a set of pixels plus its bounding ranges in RGBA space.
type colorBox struct {
	pixels []rgba8
}

// channelRanges returns the min and max of each channel across the box.
func (bx *colorBox) channelRanges() (mins, maxs [4]uint8) {
	mins = [4]uint8{255, 255, 255, 255}
	maxs = [4]uint8{0, 0, 0, 0}
	for _, p := range bx.pixels {
		ch := [4]uint8{p.r, p.g, p.b, p.a}
		for i := 0; i < 4; i++ {
			if ch[i] < mins[i] {
				mins[i] = ch[i]
			}
			if ch[i] > maxs[i] {
				maxs[i] = ch[i]
			}
		}
	}
	return
}

// longestAxis returns the channel index (0=R,1=G,2=B,3=A) with the widest range
// and the size of that range. A range of 0 means the box is a single colour.
func (bx *colorBox) longestAxis() (axis int, span int) {
	mins, maxs := bx.channelRanges()
	for i := 0; i < 4; i++ {
		d := int(maxs[i]) - int(mins[i])
		if d > span {
			span = d
			axis = i
		}
	}
	return
}

// average returns the mean colour of the box as an NRGBA palette entry.
func (bx *colorBox) average() color.NRGBA {
	if len(bx.pixels) == 0 {
		return color.NRGBA{}
	}
	var sr, sg, sb, sa int
	for _, p := range bx.pixels {
		sr += int(p.r)
		sg += int(p.g)
		sb += int(p.b)
		sa += int(p.a)
	}
	n := len(bx.pixels)
	return color.NRGBA{
		R: uint8(sr / n),
		G: uint8(sg / n),
		B: uint8(sb / n),
		A: uint8(sa / n),
	}
}

// medianCutQuantize builds a ≤maxColors palette from img and returns an
// image.Paletted mapping every pixel to the nearest palette colour.
func medianCutQuantize(img image.Image, maxColors int) *image.Paletted {
	b := img.Bounds()

	// Gather every pixel as non-premultiplied RGBA. For very large images we
	// sample the palette-building set (every 2nd/4th pixel) to bound cost; the
	// final mapping below still covers every pixel.
	stride := 1
	if b.Dx()*b.Dy() > alphaSampleBudget {
		stride = 3
	}
	pixels := make([]rgba8, 0, b.Dx()*b.Dy()/stride+1)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x += stride {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			pixels = append(pixels, rgba8{c.R, c.G, c.B, c.A})
		}
	}
	if len(pixels) == 0 {
		pixels = append(pixels, rgba8{})
	}

	// Start with one box holding all sampled colours and split greedily.
	boxes := []*colorBox{{pixels: pixels}}
	for len(boxes) < maxColors {
		// Pick the box with the widest colour spread that still has >1 pixel.
		bestIdx, bestSpan := -1, 0
		for i, bx := range boxes {
			if len(bx.pixels) < 2 {
				continue
			}
			if _, span := bx.longestAxis(); span > bestSpan {
				bestSpan = span
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break // nothing left to split
		}

		bx := boxes[bestIdx]
		axis, _ := bx.longestAxis()

		// Sort the box's pixels along its longest axis and split at the median.
		sort.Slice(bx.pixels, func(i, j int) bool {
			return channelAt(bx.pixels[i], axis) < channelAt(bx.pixels[j], axis)
		})
		mid := len(bx.pixels) / 2
		left := &colorBox{pixels: bx.pixels[:mid]}
		right := &colorBox{pixels: bx.pixels[mid:]}

		// Replace the split box with its two halves.
		boxes[bestIdx] = left
		boxes = append(boxes, right)
	}

	// Build the palette from each box's average colour.
	palette := make(color.Palette, 0, len(boxes))
	for _, bx := range boxes {
		palette = append(palette, bx.average())
	}

	// Map every pixel of the ORIGINAL image to its nearest palette entry.
	dst := image.NewPaletted(b, palette)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, img.At(x, y))
		}
	}
	return dst
}

// channelAt returns the value of the given RGBA channel of a pixel.
func channelAt(p rgba8, axis int) uint8 {
	switch axis {
	case 0:
		return p.r
	case 1:
		return p.g
	case 2:
		return p.b
	default:
		return p.a
	}
}
