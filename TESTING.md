# TESTING.md — pptx-compressor v0.1.0

This document records the test matrix for the release. It has two parts:

1. **Automated tests** — run on every build (`go test ./...`), including on the
   CI matrix (Ubuntu / Windows / macOS). These cover the container layer, the
   analyzer, the decision matrix, the encoders, and the full pipeline including
   the domain safety rules.
2. **Manual PowerPoint gate** — the release gate that cannot be automated on a
   headless CI runner: opening real, produced `.pptx` files in PowerPoint on
   Windows 11 and confirming **zero repair prompts**. Run this before tagging a
   release.

---

## 1. Automated coverage

Run everything:

```bash
go test ./...
```

| Scenario (BUILD.md §Phase 6) | Automated test | What it asserts |
|---|---|---|
| Round-trip integrity | `TestRoundTripByteIdentical` | open→write reproduces every part byte-for-byte |
| Format-conversion atomicity | `TestRenameMediaPartUpdatesRelsAndContentTypes` | rename rewrites the zip entry, `[Content_Types].xml`, and every referencing `.rels` together |
| Reject non-pptx / corrupt | `TestOpenPptxValidation` | a ZIP without the required parts is refused cleanly |
| Photo-heavy deck | `TestPipelineConvertsOpaquePngAndShrinks` | opaque PNG → JPEG, file shrinks, rels updated, output verifies |
| Transparent-logo deck | `TestDecideTransparentPngStaysPng`, `TestQuantizePreservesAlpha` | transparent PNG stays PNG (or quantizes) and keeps its alpha |
| Deck with unused media | `TestPipelineRemovesUnusedMedia` | orphaned (refCount 0) media is dropped; rels still resolve |
| Deck with embedded fonts | `TestStripEmbeddedFonts` | font part, `embeddedFontLst`, and font rels all removed together |
| EMF + animated GIF pass-through | `TestPipelinePassesVectorsAndAnimatedGifUntouched` | vector and multi-frame parts survive byte-for-byte |
| Already-optimized deck | `TestAlreadyOptimizedDeckKeepsOriginals` | never-larger guarantee: output never exceeds input |
| Never-larger fallback | `TestPipelineNeverEnlarges` | a 1×1 PNG is kept, not enlarged |
| Cancellation | `TestPipelineCancellation` | a cancelled run produces no output file |
| Alpha detection (opaque RGBA) | `TestImageHasAlpha` | a fully-opaque RGBA image reports no alpha (JPEG candidate) |
| Format detection by bytes | `TestDetectFormat` | PNG/JPEG/GIF/EMF/SVG/unknown identified from magic bytes |
| jpegli / WebP encoders run | `TestEncodeJPEG`, `TestEncodeWebP` | the CGo-free WASM encoders produce decodable output |
| Downscaler never upscales | `TestResizeNeverUpscales` | images within the cap are returned unchanged |

The large-deck / memory case from the matrix is exercised in spirit by the
pipeline tests (which decode, resize, and re-encode multi-hundred-KB images
through the worker pool). A true 200 MB deck is part of the manual gate below.

---

## 2. Manual PowerPoint gate (Windows 11 — run before release)

These require a Windows machine with PowerPoint and cannot run on CI. Build the
app (`wails build -platform windows/amd64`) or `wails dev`, then for each deck:
Open → Analyze → Compress → open the produced `<name>_compressed.pptx` in
PowerPoint.

| # | Deck | Expected result | Pass? |
|---|---|---|---|
| 1 | Photo-heavy deck | Large % saved; every slide renders identically; **no repair prompt** | ☐ |
| 2 | Transparent-logo deck | Logos keep transparency (no white boxes); no repair prompt | ☐ |
| 3 | Deck with unused media (with "remove unused" on) | File shrinks; all slides intact | ☐ |
| 4 | Deck with embedded fonts (with "strip fonts" on) | Opens; substitute fonts shown; report warns about substitution | ☐ |
| 5 | Deck with EMF + animated GIF | EMF crisp at any zoom; GIF still animates; both untouched | ☐ |
| 6 | Already-optimized deck | Mostly "kept (no gain)"; output ≤ input | ☐ |
| 7 | ~200 MB deck | Completes without running out of memory; duration reasonable; opens | ☐ |
| 8 | Password-protected / corrupt file | Clean error toast, no crash, no partial output | ☐ |

**Release gate:** every produced file in rows 1–7 must open in PowerPoint with
**zero repair prompts**. A repair prompt means a `.rels`/content-type/part
mismatch slipped through and the release must be blocked.

---

## Notes

- `go vet ./...` and `gofmt -l .` must be clean (enforced by CI).
- The WASM encoders (jpegli, WebP) require **Go 1.25+**; CI and `go.mod` are
  pinned accordingly.
