// =============================================================================
// analyzer.go — Media inventory and savings estimation (read-only analysis).
// =============================================================================
//
// The analyzer inspects a .pptx without modifying it and produces the
// AnalysisResult that backs the UI table (see types.go). For each part under
// ppt/media/ it will:
//
//   - Detect the image format from its bytes (jpeg/png/gif/emf/wmf/svg/webp/...).
//   - Decode pixel dimensions and byte size.
//   - Determine real transparency by sampling the full alpha channel — an RGBA
//     PNG that is fully opaque is treated as opaque (a JPEG-conversion candidate).
//   - Count how many slide/layout/master .rels files reference the part.
//   - Choose a proposed action and estimate the resulting size given the
//     active CompressionOptions.
//
// Skip rules baked into the analysis (never touched, always "skip"):
//   EMF / WMF / SVG vector parts and animated GIFs.
//
// This file currently contains no implementation — only this contract.
// =============================================================================

package main
