// =============================================================================
// pptx.go — PPTX archive I/O: ZIP open/rewrite, content types, .rels graph.
// =============================================================================
//
// A .pptx file is an Office Open XML package: a ZIP archive containing XML
// parts plus binary media. The parts this file will manage:
//
//   [Content_Types].xml   Declares the MIME type of every part, via extension
//                         defaults (<Default Extension="png" .../>) and
//                         per-part overrides (<Override PartName="..." .../>).
//   _rels/.rels           Package-level relationships (entry points).
//   ppt/_rels/*.rels      Relationships from the presentation to its slides.
//   ppt/slides/.../*.rels Per-slide/layout/master relationships that point at
//                         media parts under ppt/media/.
//   ppt/media/*           The embedded images we recompress.
//
// FUTURE RESPONSIBILITIES (implemented in a later session):
//   - Open a .pptx read-only and enumerate its parts.
//   - Build the relationship graph so each media part's reference count is known.
//   - Rewrite the archive: when a media part changes format, ATOMICALLY
//       (1) rename the part (e.g. image3.png -> image3.jpeg),
//       (2) update [Content_Types].xml defaults/overrides for the new extension,
//       (3) rewrite every .rels file that referenced the old part name.
//     All three must happen together or the .pptx will not open.
//   - Re-zip at the best Deflate level, writing <name>_compressed.pptx next to
//     the source. The source file is NEVER modified.
//
// This file currently contains no implementation — only this contract.
// =============================================================================

package main
