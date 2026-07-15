// =============================================================================
// analyzer.go — Media inventory and savings estimation (read-only analysis).
// =============================================================================
//
// The analyzer inspects a .pptx WITHOUT modifying it and produces the
// AnalysisResult that backs the UI table (see types.go). For every part under
// ppt/media/ it records the observed facts (format, dimensions, real alpha,
// reference count) and a proposed plan with an ESTIMATED post-compression size.
//
// The estimate is intentionally a fast heuristic — it does not actually encode
// each image (that would make analysis as slow as compression). The UI labels
// these numbers as estimates; the true sizes come from the compression run.
//
// Preset resolution (Light / Balanced / Aggressive) also lives here so both the
// analyzer's default estimate and the compression pipeline share one source of
// truth for the quality/max-edge/format defaults.
// =============================================================================

package main

import (
	"os"
	"strings"
)

// fileSize returns the size on disk of path, or 0 if it cannot be stat'd.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// hasEmbeddedFonts reports whether the package embeds fonts. PowerPoint stores
// embedded fonts as ppt/fonts/*.fntdata parts.
func hasEmbeddedFonts(p *PptxFile) bool {
	for _, e := range p.Entries {
		if strings.HasPrefix(e.name, "ppt/fonts/") && strings.HasSuffix(e.name, ".fntdata") {
			return true
		}
	}
	return false
}

// =============================================================================
// Presets — coarse quality profiles that seed the fine-grained options
// =============================================================================

// resolveOptions fills in a complete CompressionOptions from the chosen preset,
// then lets any explicitly-set fine-grained field override the preset default.
// A zero JpegQuality / MaxEdgePx / MinSizeKB means "use the preset value"; a
// non-zero value means the user moved a slider and it wins.
//
// Preset table (from BUILD.md):
//
//	Light      quality 90 / maxEdge 2560 / no PNG→JPEG
//	Balanced   quality 82 / maxEdge 1920 / convert opaque PNG
//	Aggressive quality 72 / maxEdge 1440 / convert opaque PNG + quantize transparent
func resolveOptions(opts CompressionOptions) CompressionOptions {
	// Base defaults per preset.
	var q, edge int
	convertPng := false
	quantize := false

	switch strings.ToLower(opts.Preset) {
	case "light":
		q, edge = 90, 2560
	case "aggressive":
		q, edge = 72, 1440
		convertPng = true
		quantize = true
	case "balanced", "":
		fallthrough
	default:
		q, edge = 82, 1920
		convertPng = true
	}

	// Sliders override preset defaults only when the user actually set them.
	if opts.JpegQuality == 0 {
		opts.JpegQuality = q
	}
	if opts.MaxEdgePx == 0 {
		opts.MaxEdgePx = edge
	}
	// A preset also seeds the format toggles, but only when the request did not
	// set them. Because Go zero-values booleans to false we cannot tell "unset"
	// from "explicitly false"; the frontend always sends explicit booleans, so
	// here we treat the preset as the default ONLY when the preset implies a
	// stronger setting than what arrived. To keep behaviour predictable we OR in
	// the preset's implied toggles.
	opts.ConvertOpaquePng = opts.ConvertOpaquePng || convertPng
	opts.QuantizeTransparentPng = opts.QuantizeTransparentPng || quantize

	// A sane floor for the skip threshold if none provided.
	if opts.MinSizeKB == 0 {
		opts.MinSizeKB = 10
	}
	return opts
}

// defaultAnalysisOptions is the option set the analyzer uses to estimate savings
// when no explicit options are supplied (AnalyzePptx takes only a path). It
// mirrors the Balanced preset, the app's default.
func defaultAnalysisOptions() CompressionOptions {
	return resolveOptions(CompressionOptions{Preset: "balanced"})
}

// =============================================================================
// AnalyzeMedia — inventory every media part
// =============================================================================

// AnalyzeMedia inspects each ppt/media/* part of an opened package and returns
// one MediaInfo per part, in archive order. It reads bytes only — the package
// is never modified. opts drives the proposed-action estimate.
//
// Per part it determines:
//   - Format from magic bytes (not the extension).
//   - Pixel dimensions via DecodeConfig for raster formats.
//   - Real alpha by sampling the decoded alpha channel (raster with alpha only).
//   - Reference count from the rels index (0 = orphan).
//   - A proposed action + estimated size from a dry-run of the decision logic.
func AnalyzeMedia(p *PptxFile, opts CompressionOptions) []MediaInfo {
	opts = resolveOptions(opts)

	var out []MediaInfo
	for _, name := range p.MediaParts() {
		e := p.entry(name)
		if e == nil {
			continue
		}

		info := MediaInfo{
			PartName: name,
			Bytes:    int64(len(e.data)),
			RefCount: p.RefCount(name),
		}

		format := detectFormat(e.data)
		info.Format = format

		// Dimensions and alpha only make sense for rasters we can decode.
		if isRasterFormat(format) {
			if cfg, err := decodeConfig(e.data); err == nil {
				info.Width = cfg.Width
				info.Height = cfg.Height
			}
			// Alpha requires a full decode; only PNG/WebP/GIF/BMP/TIFF can carry
			// it, and even then we sample actual pixels (see codec.go).
			if format == fmtPNG || format == fmtWebP || format == fmtBMP || format == fmtTIFF {
				if img, _, err := decodeImage(e.data); err == nil {
					info.HasAlpha = imageHasAlpha(img)
				}
			}
		}

		// Proposed action + estimate.
		info.ProposedAction, info.EstimatedBytes = estimateAction(info, opts)
		out = append(out, info)
	}
	return out
}

// =============================================================================
// estimateAction — fast dry-run of the decision matrix for the UI
// =============================================================================

// estimateAction predicts what the pipeline will do to a part and roughly how
// many bytes will remain, WITHOUT encoding anything. It mirrors the decision
// matrix in codec.go (DecideAction) closely enough to give the user a useful
// preview; the authoritative numbers come from the real run.
//
// Heuristics used:
//   - JPEG output ≈ 0.8 bytes/pixel at quality 85 / 4:2:0, scaled linearly by
//     the chosen quality relative to 85 (BUILD.md).
//   - PNG re-encode ≈ 90% of original; quantized transparent PNG ≈ 45%.
//   - Downscaling reduces the pixel count by the square of the edge ratio.
//   - The never-larger guarantee is reflected: an estimate is capped at the
//     original size, and if it would not shrink we report "kept".
func estimateAction(m MediaInfo, opts CompressionOptions) (string, int64) {
	// Per-image override wins outright.
	if ov, ok := opts.PerImageOverrides[m.PartName]; ok {
		switch strings.ToLower(ov) {
		case "skip":
			return actSkip, m.Bytes
		case "remove":
			return actRemove, 0
		}
	}

	// Below the minimum size we never touch the image.
	if m.Bytes < int64(opts.MinSizeKB)*1024 {
		return actSkip, m.Bytes
	}

	// Vectors, media and animated GIFs are always skipped.
	switch m.Format {
	case fmtEMF, fmtWMF, fmtSVG, fmtMedia, fmtUnknown:
		return actSkip, m.Bytes
	}

	// Effective pixel count after any downscale.
	pixels := int64(m.Width) * int64(m.Height)
	if pixels <= 0 {
		// Could not read dimensions — assume no gain rather than over-promise.
		return actSkip, m.Bytes
	}
	pixels = downscaledPixels(m.Width, m.Height, opts.MaxEdgePx)

	// Decide the target encoding path.
	action := actSkip
	var est int64

	switch m.Format {
	case fmtJPEG:
		action = actRecompressJPEG
		est = estJPEGBytes(pixels, opts.JpegQuality)

	case fmtGIF:
		// Single-frame GIF is treated as a PNG-style path; multi-frame handled
		// upstream (isAnimatedGIF) — here we just estimate a modest gain.
		action = actRecompressPng
		est = int64(float64(m.Bytes) * 0.9)

	case fmtPNG, fmtBMP, fmtTIFF, fmtWebP:
		if !m.HasAlpha && opts.ConvertOpaquePng {
			action = actPngToJpeg
			est = estJPEGBytes(pixels, opts.JpegQuality)
		} else if m.HasAlpha && opts.QuantizeTransparentPng {
			action = actQuantizePng
			est = int64(float64(m.Bytes) * 0.45)
		} else {
			action = actRecompressPng
			est = int64(float64(m.Bytes) * 0.9)
		}
	}

	if opts.UseWebp && isRasterFormat(m.Format) {
		action = actWebp
		// WebP is typically ~25-30% smaller than JPEG at similar quality.
		est = int64(float64(estJPEGBytes(pixels, opts.JpegQuality)) * 0.72)
	}

	// Never-larger guarantee, reflected in the estimate.
	if est <= 0 || est >= m.Bytes {
		return actKept, m.Bytes
	}
	return action, est
}

// downscaledPixels returns the pixel count after capping the longest edge at
// maxEdge (0 = no cap). Never upscales.
func downscaledPixels(w, h, maxEdge int) int64 {
	if maxEdge <= 0 {
		return int64(w) * int64(h)
	}
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return int64(w) * int64(h)
	}
	ratio := float64(maxEdge) / float64(longest)
	nw := int(float64(w) * ratio)
	nh := int(float64(h) * ratio)
	return int64(nw) * int64(nh)
}

// estJPEGBytes estimates JPEG output size: ~0.8 bytes/pixel at quality 85,
// scaled linearly by quality. Clamped to a small floor so tiny images do not
// estimate zero.
func estJPEGBytes(pixels int64, quality int) int64 {
	if quality <= 0 {
		quality = 85
	}
	bpp := 0.8 * (float64(quality) / 85.0)
	est := int64(float64(pixels) * bpp)
	if est < 1024 {
		est = 1024
	}
	return est
}
