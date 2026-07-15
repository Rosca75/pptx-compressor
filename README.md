# PPTX Compressor

A small native desktop app that shrinks PowerPoint (`.pptx`) files by
intelligently recompressing their embedded images — without touching your
original file.

Point it at a `.pptx`, review an inventory of every image inside it with an
estimated size saving, tune the options, and produce a new
`<name>_compressed.pptx` next to the original.

> **Status:** v0.1.0 — the compression engine is complete: container rewriting,
> media analysis, the decision matrix, the jpegli/PNG/WebP encoders, the
> worker-pool pipeline, and the full analysis/options/report UI.

---

## Why not just use PowerPoint's built-in compressor?

PowerPoint's **File → Compress Pictures** is a blunt instrument:

- **Fixed PPI resampling.** You pick a target PPI (96 / 150 / 220…), not an
  actual quality or pixel budget. It resamples to a print assumption, not to
  what the slide needs.
- **A mediocre encoder.** It re-saves as baseline JPEG with a fixed quality and
  no modern encoder tuning, leaving easy savings on the table.
- **No per-image control.** It is all-or-nothing (or "this picture only") with
  no way to see which images are actually heavy or to skip specific ones.
- **No transparency-aware decisions.** It won't notice that a large "transparent"
  PNG is actually fully opaque and would be far smaller as a JPEG, nor palette-
  quantize the PNGs that genuinely need transparency.

This tool analyses each image individually and applies the right treatment to
each one.

---

## Features

- **Media inventory** — lists every image under `ppt/media/` with its format,
  pixel dimensions, byte size, real transparency, and how many slides reference it.
- **Estimated savings** before you commit, per image and in total.
- **Presets** — Light / Balanced / Aggressive, with fine-grained overrides.
- **Modern JPEG encoding** via [jpegli](https://github.com/gen2brain/jpegli)
  (CGo-free) for better quality-per-byte.
- **Transparency-aware format choices** — convert opaque PNGs to JPEG; palette-
  quantize PNGs that truly use transparency.
- **High-quality downscaling** (CatmullRom) with a max-longest-edge cap.
- **Cleanup** — remove unused media parts and (optionally) strip embedded fonts.
- **Per-image overrides** — skip or remove individual images from the table.
- **Safe by design** — never enlarges an image (keeps the original bytes if
  re-encoding would be bigger) and **never modifies your source file**.

---

## Screenshots

_Placeholder — screenshots will be added once the UI is populated._

---

## Download & build

### Download (recommended)

Grab the latest `pptx-compressor.exe` from the
[Releases](../../releases) page (Windows x64). No installer — just run it.
WebView2 ships with Windows 10/11.

### Build from source

Prerequisites: **Go 1.25+** (required by the jpegli encoder), the **Wails v2
CLI**, and **Node.js 20+** (used by the Wails toolchain).

```bash
# Install the Wails CLI once
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Run in dev mode (live reload)
wails dev

# Produce a Windows release binary → build/bin/pptx-compressor.exe
wails build -platform windows/amd64
```

To verify Go compilation without opening a window:

```bash
go build ./...
```

---

## Option reference

| Option | What it does |
|---|---|
| **Compression preset** | Light / Balanced / Aggressive — seeds defaults for the options below. |
| **JPEG quality** | Target quality (1–100) for JPEG re-encoding. |
| **Max longest edge (px)** | Downscales images whose longest edge exceeds this. `0` = no downscaling. |
| **Skip images under (KB)** | Ignores images smaller than this threshold. |
| **Convert opaque PNG → JPEG** | Re-encodes fully-opaque PNGs as JPEG (usually much smaller). |
| **Quantize transparent PNG** | Palette-quantizes PNGs that truly use transparency, keeping the alpha. |
| **Output WebP** | Emits WebP instead of JPEG/PNG. **Opt-in** — see compatibility below. |
| **Remove unused media parts** | Drops images no slide references. |
| **Strip embedded fonts** | Removes embedded font parts to save space. |
| **Per-image skip/remove** | Override the plan for a single image from the table. |

### Preset defaults

Presets seed the sliders and format toggles; any control you touch afterwards
overrides the preset.

| Preset | JPEG quality | Max longest edge | Opaque PNG → JPEG | Quantize transparent PNG |
|---|---|---|---|---|
| **Light** | 90 | 2560 px | off | off |
| **Balanced** (default) | 82 | 1920 px | on | off |
| **Aggressive** | 72 | 1440 px | on | on |

---

## Compatibility note on WebP

WebP output is **off by default**. PowerPoint can only display WebP images on
**Microsoft 365 version 2402 (February 2024) and later**. Older Office
versions, and files shared with people on older builds, will show missing
images. Enable WebP only if you are certain every viewer is on a recent M365.

The default JPEG/PNG output opens in all supported versions of PowerPoint.

---

## License

MIT — see [LICENSE](LICENSE).
