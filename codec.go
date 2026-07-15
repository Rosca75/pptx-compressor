// =============================================================================
// codec.go — Alpha detection and image encoders.
// =============================================================================
//
// This file will centralise all image decoding, alpha analysis and encoding so
// the pipeline (compressor.go) stays format-agnostic.
//
// FUTURE RESPONSIBILITIES (implemented in a later session):
//   - Alpha detection: decode the image and sample the FULL alpha channel to
//     decide whether it really uses transparency. Metadata alone is not
//     trusted — an RGBA image can be entirely opaque.
//   - JPEG encoding via jpegli (github.com/gen2brain/jpegli) — a CGo-free WASM
//     encoder that is the approved exception to the no-CGo rule. Used for photos
//     and for opaque-PNG → JPEG conversion.
//   - PNG encoding via the stdlib image/png, combined with median-cut palette
//     quantization for images that genuinely use transparency (keeps alpha).
//   - Optional WebP output (opt-in only — PowerPoint reads WebP solely on
//     Microsoft 365 version 2402 and later).
//
// This file currently contains no implementation — only this contract.
// =============================================================================

package main
