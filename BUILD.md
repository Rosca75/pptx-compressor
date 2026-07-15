# BUILD.md — pptx-compressor v1 Implementation Plan

> Instructions for Claude Code. Prerequisite: the repository skeleton exists (bootstrap prompt completed, `go build ./...` passes, CLAUDE.md read in full). Work phase by phase; each phase ends with a compiling build and a commit. Do not start a phase before the previous one is committed.

---

## Dependencies (add in Phase 2, justify nothing else without asking)

| Module | Purpose | Note |
|---|---|---|
| `github.com/gen2brain/jpegli` | JPEG encoder (libjxl's jpegli via WASM/wazero) | ~35% smaller than libjpeg at equal quality, output is standard JPEG, **CGo-free** |
| `github.com/gen2brain/webp` | WebP encode (opt-in mode only) | CGo-free (wazero) |
| `golang.org/x/image` | `draw.CatmullRom` downscaling, format decode helpers | already familiar from dedup-photos |

Everything else is stdlib: `archive/zip`, `compress/flate`, `image`, `image/png`, `image/jpeg` (decode), `image/gif` (detection), `encoding/xml`, `regexp`, `sync`.

---

## Phase 1 — PPTX container layer (`pptx.go`)

Goal: open, model, and rewrite a .pptx without touching images yet.

1. `type PptxFile struct` holding: source path, ordered slice of `zipEntry{name string, data []byte, method uint16}` (order preservation matters — write entries back in original order), parsed content types, rels index.
2. `OpenPptx(path string) (*PptxFile, error)` — read all entries into memory (decks are typically < 500 MB; document this assumption). Validate it is a pptx: must contain `[Content_Types].xml` and `ppt/presentation.xml`.
3. Content types: parse `[Content_Types].xml` minimally with `encoding/xml` into `Default` (extension→type) and `Override` (partname→type) lists. Provide `EnsureDefault(ext, contentType)`.
4. Rels graph: `BuildRelsIndex()` — parse every `*.rels` file; produce `map[mediaPartName][]relRef` where `relRef` records the .rels file and relationship Id. Media parts = entries under `ppt/media/`.
5. `RenameMediaPart(oldName, newName string)` — renames the zip entry, rewrites `Target` in every referencing `.rels` (string-level replace within the XML attribute, preserving the rest of the file byte-for-byte), ensures the content-type default for the new extension exists.
6. `RemoveMediaPart(name string)` — only permitted when refCount == 0 (unused media); delete entry.
7. `WritePptx(outPath string) error` — write all entries with `zip.Writer`; register `flate.BestCompression` via `zip.RegisterCompressor`. Never overwrite an existing file at outPath without an explicit overwrite flag.
8. **Tests** (`pptx_test.go`): build a tiny synthetic pptx fixture in code (zip with minimal `[Content_Types].xml`, one slide, one rels, one 1×1 PNG); round-trip open→write and assert byte-identical part contents; test rename updates rels + content types.

Commit: `feat: pptx container layer (zip, content types, rels graph)`

## Phase 2 — Analyzer (`analyzer.go`, `codec.go` detection half)

1. `AnalyzeMedia(p *PptxFile) []MediaInfo` — for each `ppt/media/*` entry:
   - Format from magic bytes (not extension): PNG, JPEG, GIF, BMP, TIFF, EMF, WMF, SVG, WebP, media (mp4/audio → skip, report as "media, not processed in v1").
   - Dimensions via `image.DecodeConfig` (register decoders); EMU-free in v1.
   - `hasAlpha`: for PNG/WebP, decode and scan the alpha channel — sample every pixel of images ≤ 4 MP, every 4th pixel above; alpha found = any value < 255. For formats without alpha channel, false. **Metadata alone is not sufficient** (a PNG can be RGBA yet fully opaque — that is precisely the case we convert to JPEG).
   - `refCount` from the rels index; 0 = unused (PowerPoint keeps orphans surprisingly often).
   - `proposedAction` + `estimatedBytes` from a dry-run of the decision matrix (Phase 3) — estimate with a fast heuristic (e.g., quality-85 JPEG ≈ 0.8 bytes/pixel at 4:2:0) rather than actually encoding; label it as an estimate in the UI.
2. Wire `AnalyzePptx` in `app.go`: open file, analyze, return `AnalysisResult` (total size, media size, per-image rows, count of unused parts, embedded-fonts presence `ppt/fonts/*.fntdata`).
3. `GetImagePreview(partName)` — decode, thumbnail to ≤ 160 px longest edge, return base64 JPEG (reuse dedup-photos' pattern).

Commit: `feat: media analyzer (format, dimensions, alpha detection, usage graph)`

## Phase 3 — Decision matrix + encoders (`codec.go`, `resize.go`)

The heart. Implement `DecideAction(m MediaInfo, opts CompressionOptions) Action` exactly as follows:

| Input | Condition | Action |
|---|---|---|
| any | bytes < opts.minSizeKB·1024 | **Skip** |
| any | per-image override present | override wins (Skip / Remove / Force) |
| EMF, WMF, SVG | always | **Skip** (vector — rasterizing loses fidelity) |
| GIF | multi-frame (animated) | **Skip** |
| GIF | single frame | treat as PNG path |
| JPEG | always | re-encode jpegli at opts.jpegQuality, downscale if needed |
| PNG/BMP/TIFF/WebP | `!hasAlpha` and opts.convertOpaquePng | convert → **JPEG** (jpegli), rename part |
| PNG | `hasAlpha` and opts.quantizeTransparentPng | downscale if needed → median-cut quantize to ≤ 256-color palette with alpha → `image/png` BestCompression (paletted PNG with tRNS) |
| PNG | `hasAlpha`, quantization off | downscale if needed → re-encode `image/png` BestCompression |
| any raster | opts.useWebp | encode WebP instead of JPEG/PNG (lossy for opaque, lossy-with-alpha for transparent), rename part |

Rules that apply to every path:
- **Downscale first**: if longest edge > opts.maxEdgePx, resize with `draw.CatmullRom` preserving aspect ratio. Never upscale.
- **Never-larger guarantee**: after encoding, if `len(newBytes) >= len(originalBytes)`, keep the original bytes and report action "kept (no gain)".
- **Remove** (per-image override): replace part content with an embedded 1×1 transparent PNG of the same format family so every relationship stays valid — never delete a referenced part.
- Presets map: Light = quality 90 / maxEdge 2560 / no PNG→JPEG; Balanced = 82 / 1920 / convert opaque PNG; Aggressive = 72 / 1440 / convert + quantize. Sliders override presets.
- Quantizer: implement median-cut over RGBA in `codec.go` (~120 lines, comment heavily) rather than adding a dependency; output `image.Paletted` — Go's PNG encoder writes the tRNS chunk automatically for palettes with alpha.

Unit tests: opaque RGBA PNG → JPEG decision; transparent PNG stays PNG; tiny image skipped; never-larger fallback; animated GIF skipped (build fixtures in code).

Commit: `feat: decision matrix, jpegli/png/webp encoders, downscaler`

## Phase 4 — Compression pipeline (`compressor.go`)

1. Worker pool (`runtime.NumCPU()` workers, mirror dedup-photos' `parallel.go` pattern) consuming media parts; each worker: decide → decode → transform → encode → propose `(newBytes, newName)`.
2. Single coordinator goroutine applies results to the `PptxFile` (renames/rels rewrites are not thread-safe by design — document this).
3. Global job state guarded by mutex, exactly like dedup-photos scan state: `StartCompression` spawns the job, `GetProgress` returns counts + bytesBefore/bytesAfter + per-file log, `CancelCompression` sets a context cancel; workers check `ctx.Err()` between images.
4. Post-processing steps, each behind its option flag: remove unused media (refCount 0), strip `ppt/fonts/*.fntdata` **and** the `embeddedFontLst` element in `ppt/presentation.xml` plus its rels (warn in report: recipients without the fonts will see substitutions), regenerate/strip `docProps/thumbnail.*`.
5. Output: `<dir>/<name>_compressed.pptx`; final report struct: before/after totals, per-image rows (action, before, after, %), errors. On any fatal error, delete the partial output file.
6. **Round-trip safety check**: after writing, re-open the output with `OpenPptx` and assert every `.rels` target resolves to an existing part. If not, fail loudly and keep nothing.

Commit: `feat: compression pipeline, progress polling, safety checks`

## Phase 5 — Frontend

1. `analyze.js` + `table.js`: file picker → AnalyzePptx → sortable table (thumbnail, name, format, dimensions, size, alpha badge, refs, proposed action, est. savings); row-level dropdown for override (Auto / Skip / Remove).
2. `options.js`: preset radio (Light/Balanced/Aggressive) driving quality + maxEdge sliders; threshold input; checkboxes for opaque-PNG→JPEG, quantize transparent PNG, remove unused media, strip fonts; WebP toggle **with inline warning** ("Requires PowerPoint Microsoft 365 version 2402 or later — older Office cannot display WebP").
3. `compress.js`: start → 400 ms polling loop → progress bar with current file + running bytes saved → cancel button.
4. `report.js`: final summary card (before/after/% saved), per-image results table, "Open folder" button (`OpenOutputFolder`).
5. Keep every `window.go` call inside `api.js`. Toast errors via `components.js`.

Commit: `feat: analysis table, options panel, progress and report UI`

## Phase 6 — Hardening & release

1. Manual test matrix (document results in `TESTING.md`): photo-heavy deck, transparent-logo deck, deck with unused media, deck with embedded fonts, deck containing EMF + animated GIF (must pass through untouched), already-optimized deck (expect "kept, no gain" dominance), 200 MB deck (memory + duration), password-protected/corrupt file (clean error).
2. Verify output opens in PowerPoint with zero repair prompts — this is the release gate.
3. Update README (option reference, screenshots), bump CLAUDE.md line counts, tag `v0.1.0`, confirm release workflow produces `pptx-compressor.exe`.

Commit: `chore: v0.1.0`

---

## Deferred to v2 (do not implement now, keep code open to it)

- **Per-image smart sizing from slide XML**: parse `a:ext cx/cy` (EMU, 914,400 per inch) for each image usage to compute the true rendered size, then downscale to a target effective PPI (150/220) per image instead of a global max edge.
- Delete cropped areas (`a:srcRect` pre-cropping before re-encode).
- Video/audio transcoding, unused slide-layout/master pruning, docx/xlsx support.
