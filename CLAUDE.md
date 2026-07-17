# CLAUDE.md — PPTX Compressor

> This file is the single source of truth for Claude Code working on this project.
> Read it fully before making any change. Follow every rule without exception.

---

## 1. Project Overview

**PPTX Compressor** is a Go-based tool that shrinks PowerPoint (`.pptx`) files by
intelligently recompressing their embedded images. It is packaged as a **native
desktop application** using [Wails v2](https://wails.io): a native Windows window
(via WebView2) with a Go backend and a web-based UI embedded directly in the
binary — no browser, no localhost port, no npm build step.

A `.pptx` is a ZIP archive of XML parts plus binary media. The app opens one,
inventories the images under `ppt/media/`, lets the user choose compression
options, recompresses the images that will actually get smaller, and writes a
new `<name>_compressed.pptx` next to the original — **the source is never
modified.**

**Owner profile:**
- Running on **Windows 11**, Go installed via `winget install GoLang.Go`
- Comfortable with Python, TypeScript/JS, web frontends — **not a Go expert**
- **Go code must be heavily commented** — explain every function and non-obvious block
- Build command: `wails build -platform windows/amd64`
- Dev mode (live reload): `wails dev`
- Prerequisites: Go 1.25+ (jpegli minimum), Wails CLI v2
  (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`),
  Node.js 20+ (required by the Wails toolchain), WebView2 (pre-installed on Windows 10/11)

---

## 2. Repository Structure

```
pptx-compressor/
├── main.go            91 lines   Wails app entry point (wails.Run, //go:embed)
├── app.go            246 lines   App struct — all public methods bound to the JS frontend
├── types.go          243 lines   Shared API types + global background-job state
├── pptx.go           604 lines   ZIP open/rewrite, content types, .rels graph
├── analyzer.go       284 lines   media inventory + savings estimation + presets
├── compressor.go     496 lines   worker-pool compression pipeline + post-processing
├── codec.go          642 lines   format detection, alpha, encoders, decision matrix, median-cut
├── video.go                      video detection, placeholder MP4, ffmpeg MP4 recompression
├── video_windows.go              Windows-only: hide ffmpeg's console window (+.exe suffix)
├── video_other.go                non-Windows no-op counterparts
├── resize.go          59 lines   CatmullRom downscaling
├── pptx_test.go      293 lines   container-layer tests (synthetic fixtures)
├── codec_test.go     143 lines   detection / alpha / analyzer tests
├── decision_test.go  174 lines   decision-matrix and encoder tests
├── compressor_test.go 421 lines  end-to-end pipeline / scenario tests
├── video_test.go                 video detection / decision / slide-order tests
├── wails.json                    Wails config (name, author, empty frontend:* fields)
├── go.mod / go.sum
├── .github/workflows/
│   ├── ci.yml                    vet + gofmt + build matrix (ubuntu/windows/macos)
│   └── release.yml               tag v* → wails build windows/amd64 → release
└── static/                       ← active frontend (embedded via //go:embed all:static)
    ├── index.html    188 lines   3-zone layout: top bar / analysis table / options panel
    ├── css/
    │   ├── base.css        67 lines   Design tokens, reset, typography
    │   ├── layout.css     143 lines   3-zone CSS grid
    │   ├── table.css       86 lines   Analysis table + badges + thumbnails + sort
    │   └── components.css 179 lines   Buttons, controls, toast, confirm, report, warning
    └── js/
        ├── app.js          16 lines   Entry point — imports modules, wires init()
        ├── state.js        37 lines   Shared state object (single source of truth)
        ├── api.js          82 lines   All window.go.main.App.* calls (isolation layer)
        ├── helpers.js      42 lines   Pure formatting utilities
        ├── components.js   59 lines   showToast(), showConfirm()
        ├── analyze.js      55 lines   File picker + Analyze flow
        ├── options.js     121 lines   Options panel: presets, read/enable, WebP warning
        ├── compress.js    108 lines   StartCompression + progress polling + Cancel
        ├── table.js       176 lines   Analysis table: previews, override, sorting
        └── report.js       96 lines   Before/after report card + per-image results
```

> **Status:** v0.1.0 — the business logic is complete. `go build ./...` and
> `go test ./...` pass; CI enforces `go vet` and `gofmt`. Requires **Go 1.25+**
> (the jpegli encoder's minimum). The v2 deferral list at the end of BUILD.md is
> the roadmap from here.

---

## 3. Architecture — Wails v2 Desktop App

### How it works

Wails does **not** run an HTTP server. There is no TCP port, no `localhost:8080`,
no `fetch()` calls. Instead:

1. `main.go` embeds the `static/` directory with `//go:embed all:static`
2. Wails opens a native Windows window and loads `static/index.html` inside it
3. Wails injects `window.go` into the page — a JS object with one method per bound Go function
4. The frontend calls `window.go.main.App.MethodName(args)` which returns a **Promise**
5. Go return values (structs, maps) are automatically serialised to JS objects
   using the `json:` struct tags in `types.go`

### Go ↔ JavaScript bridge

| Frontend call (via `api.js`)      | Bound Go method                                   |
|-----------------------------------|---------------------------------------------------|
| `apiSelectPptxFile()`             | `App.SelectPptxFile() (string, error)`            |
| `apiAnalyzePptx(path)`            | `App.AnalyzePptx(path) AnalysisResult`            |
| `apiStartCompression(req)`        | `App.StartCompression(req) map[string]string`     |
| `apiGetProgress()`                | `App.GetProgress() ProgressResult`                |
| `apiCancelCompression()`          | `App.CancelCompression() map[string]string`       |
| `apiGetImagePreview(partName)`    | `App.GetImagePreview(partName) string`            |
| `apiOpenOutputFolder(path)`       | `App.OpenOutputFolder(path) error`                |

### Long-running work — StartX / GetProgress / Cancel

Compression can take seconds to minutes. It runs in a background goroutine using
the polling model:

1. `StartCompression(req)` resets shared state, launches the goroutine, returns immediately.
2. The frontend polls `GetProgress()` every ~500ms and updates the progress bar.
3. `CancelCompression()` cancels the goroutine's context; the pipeline stops between images.
4. When `GetProgress()` reports state `"done"`, the frontend renders the before/after report.

### Special cases

**Image previews** — `GetImagePreview(partName)` returns a base64 string.
The frontend sets: `img.src = "data:image/png;base64," + result`.

---

## 4. Go Files Reference

### `main.go` — entry point

Calls `wails.Run()` with the `App` struct bound. Embeds `static/` via
`//go:embed all:static`. Window: 1280×900px, minimum 900×600px. Do not change
window dimensions without a deliberate reason.

### `app.go` — Wails-bound methods

All public methods on `*App` are automatically callable from JavaScript.

| Method | Signature | Purpose |
|---|---|---|
| `SelectPptxFile` | `() (string, error)` | Native "open file" dialog filtered to `.pptx` |
| `AnalyzePptx` | `(path string) AnalysisResult` | Read-only media inventory + savings estimate |
| `StartCompression` | `(req CompressionRequest) map[string]string` | Launch the background job |
| `GetProgress` | `() ProgressResult` | Poll job status/results |
| `CancelCompression` | `() map[string]string` | Cancel the active job |
| `GetImagePreview` | `(partName string) string` | Base64 thumbnail of one media part |
| `OpenOutputFolder` | `(path string) error` | Reveal the result in the OS file manager |

### `types.go` — types and state only

No logic. Contains:
- The shared API types: `MediaInfo`, `AnalysisResult`, `CompressionOptions`,
  `CompressionRequest`, `ProgressResult`.
- Global background-job state: `jobMutex`, `jobProgress`, `jobCancel`.

### Business-logic files (stubs until BUILD.md)

| File | Purpose |
|---|---|
| `pptx.go` | ZIP open/rewrite, `[Content_Types].xml`, the `_rels` relationship graph |
| `analyzer.go` | Media inventory, alpha/format detection, savings estimation |
| `compressor.go` | Worker-pool pipeline that re-encodes eligible images |
| `codec.go` | Alpha-channel detection + JPEG (jpegli) / PNG / WebP encoders |
| `resize.go` | CatmullRom high-quality downscaling |

---

## 5. Frontend Architecture

The frontend is vanilla-JS ES modules under `static/js/` and CSS under
`static/css/`. `static/index.html` loads `js/app.js` via
`<script type="module" src="/js/app.js">`. **Zero npm dependencies, no build
step** — the corporate proxy blocks npm.

**`api.js` is the isolation layer** — it wraps every `window.go.main.App.*`
call. No other module touches `window.go` directly.

### Module map

```
static/js/
├── app.js        Entry point — imports modules, wires init() on DOMContentLoaded
├── state.js      Shared state object (single source of truth)
├── api.js        All window.go.main.App.* calls (isolation layer)
├── helpers.js    Pure formatting utilities (formatBytes, formatSavings, ...)
├── components.js showToast(), showConfirm()
├── analyze.js    File picker + Analyze flow → renderTable
├── options.js    Options panel: read values, enable/disable controls
├── compress.js   StartCompression + GetProgress polling loop + Cancel
├── table.js      Renders the media analysis table
└── report.js     Renders the before/after report card
```

### UI layout — 3 zones

```
┌────────────────────────────────────────────────────────────────┐
│  Top Bar: [file path][Open…]  [Analyze][Compress][Cancel]      │
│  [━━━━━━━━━ progress bar (during compression) ━━━━━━━━━━━]     │
├───────────────────────────────────────────┬────────────────────┤
│  Main Area                                 │  Options Panel     │
│  Analysis table (one row per media part)   │  preset, quality,  │
│  + before/after report card                │  max edge, min     │
│                                            │  size, format &    │
│                                            │  cleanup toggles   │
└───────────────────────────────────────────┴────────────────────┘
```

---

## 6. PPTX Domain Rules

These rules are non-negotiable. Breaking any of them can corrupt the output or
the user's data.

1. **A `.pptx` is a ZIP.** It contains `[Content_Types].xml`, a `_rels`
   relationship graph, and parts. Media (images) live under `ppt/media/`.
2. **Format conversion is atomic across three edits.** When a media part
   changes format (e.g. `image3.png` → `image3.jpeg`) you MUST, together:
   1. rename the part inside the archive,
   2. update `[Content_Types].xml` (the extension `Default` and/or per-part
      `Override`) so the new extension has a declared content type,
   3. rewrite **every** `.rels` file that references the old part name.
   If any one of these is missed, PowerPoint will refuse to open the file.
4. **Never enlarge.** If re-encoding produces bytes greater than or equal to the
   original, keep the **original** bytes. Compression must only ever shrink.
5. **Never modify the source file.** Output is always a new
   `<name>_compressed.pptx` written next to the original.
6. **Always skip vectors and animated GIFs.** EMF, WMF and SVG parts are vector
   graphics; animated GIFs are multi-frame. These are never re-encoded.
   Videos are skipped by the image pipeline too; they are only touched by the
   opt-in video options — "remove videos" swaps the bytes for a tiny embedded
   placeholder MP4 (relationships/content types stay intact), and MP4
   compression shells out to an external `ffmpeg` executable (CGo stays
   forbidden; without ffmpeg the option is disabled and originals are kept).
7. **Alpha detection samples the full alpha channel.** Do not trust the format
   or metadata — an RGBA PNG can be entirely opaque (a JPEG-conversion
   candidate). Decide transparency by scanning actual pixels.
8. **WebP is opt-in.** Only Microsoft 365 version 2402 and later can read WebP
   in PowerPoint, so WebP output is off by default.

---

## 7. Coding Rules

1. **Read before writing.** Read a file before modifying it. Never assume its contents.
2. **stdlib-first.** Prefer the Go standard library. Reach for a dependency only
   when the stdlib genuinely cannot do the job.
3. **CGo is forbidden.** The corporate build environment cannot compile CGo. The
   sanctioned exception mechanism is CGo-free WASM libraries from
   `github.com/gen2brain` (e.g. `jpegli` for JPEG encoding).
4. **New dependencies require justification.** Do not add a module without a
   clear, stated reason and owner sign-off. The planned set is: `jpegli`
   (JPEG), `golang.org/x/image/draw` (CatmullRom resize), and Wails itself.
5. **No HTTP, no `fetch()`.** All Go ↔ JS communication goes through
   `window.go.main.App.*` Promises.
6. **Asset paths have no `/static/` prefix.** `fs.Sub` strips it — a file at
   `static/css/base.css` loads as `/css/base.css`.
7. **`static/` is the active frontend directory.** There is no `frontend/` directory.
8. **State lives in `state.js` only.** No shared module-level state elsewhere.
9. **Wails calls live in `api.js` only.** No other module calls `window.go.*`.
10. **Comment all Go code.** The owner is not a Go expert. Explain every
    non-obvious construct.
11. **Test after every change.** Run `go build ./...`, then `wails dev` and
    verify in the native window.
