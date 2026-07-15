// =============================================================================
// resize.go — High-quality image downscaling.
// =============================================================================
//
// Downscaling is separated from encoding so the pipeline can cap an image's
// longest edge (CompressionOptions.MaxEdgePx) before handing pixels to codec.go.
//
// FUTURE RESPONSIBILITIES (implemented in a later session):
//   - Downscale an image so its longest edge fits within MaxEdgePx, preserving
//     aspect ratio, using golang.org/x/image/draw with the CatmullRom kernel
//     for high-quality resampling.
//   - Never upscale — if the image is already within the limit, return it as-is.
//
// This file currently contains no implementation — only this contract.
// =============================================================================

package main
