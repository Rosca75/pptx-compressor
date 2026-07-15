// =============================================================================
// resize.go — High-quality image downscaling.
// =============================================================================
//
// Downscaling is separated from encoding so the pipeline can cap an image's
// longest edge (CompressionOptions.MaxEdgePx) before handing pixels to codec.go.
// Resampling uses golang.org/x/image/draw with the CatmullRom kernel — a cubic
// filter that gives sharp, artefact-free results when shrinking photos and
// graphics.
// =============================================================================

package main

import (
	"image"

	xdraw "golang.org/x/image/draw"
)

// resizeToMaxEdge returns a copy of img scaled so its longest edge is at most
// maxEdge pixels, preserving aspect ratio. It NEVER upscales: if the image
// already fits (or maxEdge <= 0), the original image is returned unchanged and
// the returned bool is false ("did not resize").
//
// The returned bool lets the caller know whether any work happened, which
// matters for the never-larger guarantee: an image that was not resized and not
// re-encoded would simply be kept as-is.
func resizeToMaxEdge(img image.Image, maxEdge int) (image.Image, bool) {
	if maxEdge <= 0 {
		return img, false
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge || longest == 0 {
		return img, false // already within the cap — never upscale
	}

	// Scale both dimensions by the same ratio to preserve aspect ratio.
	ratio := float64(maxEdge) / float64(longest)
	nw := int(float64(w) * ratio)
	nh := int(float64(h) * ratio)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	// Draw into a fresh RGBA canvas using the CatmullRom high-quality kernel.
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst, true
}
