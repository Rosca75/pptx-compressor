# build/ — Wails packaging assets

This directory holds the app icon and Windows manifest used by `wails build`.
It replaces the default Wails **"W"** icon with the PPTX Compressor logo.

Wails picks these files up automatically at build time — **no `wails.json`
changes are needed.**

## What's here

| File | Used for |
|------|----------|
| `appicon.png` | 1024×1024 master icon (Linux build + Wails' icon source) |
| `appicon.svg` | Editable vector source of the logo (source of truth) |
| `windows/icon.ico` | Windows `.exe` icon, taskbar, and window title bar (multi-res: 16/24/32/48/64/128/256) |
| `windows/info.json` | Version/company metadata stamped into the `.exe` |
| `windows/wails.exe.manifest` | Windows manifest: DPI awareness + visual styles |
| `icongen/` | Self-contained Go tool that rasterizes the icon (see below) |

Both `appicon.png` and `windows/icon.ico` are **committed binaries**, so a fresh
clone builds with the real icon straight away — you do not need any image tools
installed.

## The logo

A white presentation slide on a PowerPoint-orange rounded square, with two block
arrows converging vertically = *"compress the slide."* The vector source lives in
`appicon.svg` and is the thing to edit when the design changes.

## Regenerating the binaries after editing the logo

The binaries are produced by `build/icongen`, a small pure-Go program (no CGo, no
ImageMagick — it uses `golang.org/x/image`, matching the project's dependency
rules). It draws the logo in the same `0..1024` coordinate space as `appicon.svg`
and writes both outputs deterministically.

`icongen` is its **own Go module**, so it is excluded from the app's
`go build ./...`, `go test ./...`, and CI matrix — it never affects the app build.

1. Edit `appicon.svg` (the design), then mirror any shape/colour changes in
   `icongen/main.go` so the rasterizer matches.
2. Regenerate and copy the outputs into place:

   ```bash
   cd build/icongen
   go run .                       # writes appicon.png + icon.ico here
   mv appicon.png ../appicon.png
   mv icon.ico    ../windows/icon.ico
   ```

3. Rebuild the app:

   ```bash
   wails build -platform windows/amd64
   ```

> Tip: if you'd rather not keep `icongen` and `main.go` in sync by hand, you can
> instead rasterize `appicon.svg` with any SVG→PNG tool at 1024×1024 and convert
> to a multi-resolution `.ico` — the committed generator is just the convenient,
> tool-free path.
