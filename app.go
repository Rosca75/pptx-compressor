// =============================================================================
// app.go — Wails App struct with all methods exposed to the JS frontend.
// =============================================================================
//
// Every public (exported) method on *App is callable from JavaScript via:
//   await window.go.main.App.MethodName(args...)
//
// Wails automatically:
//   - Converts JS objects → Go structs (using the `json:` tags in types.go)
//   - Returns Go structs → JS objects (same serialisation)
//   - Runs each call in its own goroutine on the Go side
//
// The compression pipeline uses a background goroutine; JS polls GetProgress()
// every ~500ms to check progress and retrieve the final before/after report.
// This is the StartX / GetProgress / Cancel polling model.
//
// SKELETON NOTE: the methods below are stubs. They compile and return safe
// placeholder values so the UI can be wired end-to-end. The real logic lives
// in a later BUILD.md session — each method's doc comment states its future
// contract so callers can be written against it now.
// =============================================================================

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	// Wails runtime — used for native dialogs and OS integration.
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application struct. All exported methods on *App are
// exposed to the frontend JavaScript as window.go.main.App.MethodName().
type App struct {
	ctx context.Context // Wails context; available after startup().

	// mu guards the cached analysis below. Analysis runs on one goroutine and
	// GetImagePreview may run on another, so the shared package is protected.
	mu sync.Mutex

	// analyzed holds the most recently analysed package, kept in memory so
	// GetImagePreview can decode a thumbnail without re-reading the file.
	analyzed *PptxFile
}

// NewApp creates and returns a new App instance. Called from main.go.
func NewApp() *App {
	return &App{}
}

// startup is invoked by Wails after the window is created.
// The ctx allows calling Wails runtime APIs (file dialogs, events, etc.).
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// =============================================================================
// SelectPptxFile — native "open file" dialog filtered to .pptx
// =============================================================================

// SelectPptxFile opens the native OS file picker filtered to PowerPoint files
// and returns the absolute path of the chosen .pptx, or an empty string if the
// user cancelled.
//
// It opens the native OS "open file" dialog via the Wails runtime.
func (a *App) SelectPptxFile() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select a PowerPoint file",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "PowerPoint (*.pptx)", Pattern: "*.pptx"},
		},
	})
}

// =============================================================================
// AnalyzePptx — inventory the media parts (read-only)
// =============================================================================

// AnalyzePptx opens the .pptx at path (a ZIP archive) without modifying it,
// inventories every media part under ppt/media/, and returns an AnalysisResult
// describing each image and the estimated savings.
//
// It opens the archive, builds the relationship graph, inventories every media
// part, and computes an estimated post-compression size using the default
// (Balanced) options. The opened package is cached so GetImagePreview can serve
// thumbnails without re-reading the file. The source file is never modified.
func (a *App) AnalyzePptx(path string) AnalysisResult {
	p, err := OpenPptx(path)
	if err != nil {
		return AnalysisResult{Path: path, Media: []MediaInfo{}, Error: err.Error()}
	}
	if err := p.BuildRelsIndex(); err != nil {
		return AnalysisResult{Path: path, Media: []MediaInfo{}, Error: err.Error()}
	}

	opts := defaultAnalysisOptions()
	media := AnalyzeMedia(p, opts)

	// Aggregate totals for the summary row.
	var totalMedia, totalEst int64
	var unused, videos int
	for _, m := range media {
		totalMedia += m.Bytes
		totalEst += m.EstimatedBytes
		// "Unused" now means the image is placed nowhere (a true orphan OR a
		// stale relationship that no slide/layout/master actually uses), not
		// merely RefCount == 0. See usageLabel / MediaPlacement.
		if isUnusedUsage(m.Usage) {
			unused++
		}
		if m.IsVideo {
			videos++
		}
	}

	// Cache the package for subsequent preview requests.
	a.mu.Lock()
	a.analyzed = p
	a.mu.Unlock()

	return AnalysisResult{
		Path:             path,
		Media:            media,
		FileBytes:        fileSize(path),
		TotalBytes:       totalMedia,
		EstimatedBytes:   totalEst,
		UnusedCount:      unused,
		VideoCount:       videos,
		FfmpegAvailable:  ffmpegAvailable(),
		HasEmbeddedFonts: hasEmbeddedFonts(p),
	}
}

// =============================================================================
// StartCompression — launch the background compression job
// =============================================================================

// StartCompression launches compression for req in a background goroutine and
// returns immediately with {"status":"running"}. The frontend then polls
// GetProgress() until the state is "done", "cancelled" or "error".
//
// It resets the shared job state, creates a cancellable context (whose cancel
// func is stored in jobCancel), and launches the worker-pool pipeline in a
// background goroutine. It returns immediately; the frontend polls GetProgress.
// The output is <name>_compressed.pptx next to the source; the source is never
// touched.
func (a *App) StartCompression(req CompressionRequest) map[string]string {
	// Create a fresh cancellable context for this run and publish its cancel.
	ctx, cancel := context.WithCancel(context.Background())

	jobMutex.Lock()
	jobProgress = ProgressResult{State: "running"}
	jobCancel = cancel
	jobMutex.Unlock()

	// Run the pipeline on a background goroutine so this call returns at once.
	go runCompression(ctx, req)

	return map[string]string{"status": "running", "message": "Compression started"}
}

// =============================================================================
// GetProgress — poll the current job status
// =============================================================================

// GetProgress returns a snapshot of the running (or last) compression job.
// Called by the frontend on a timer while a job is active.
//
// FUTURE CONTRACT: return a copy of jobProgress under jobMutex.
func (a *App) GetProgress() ProgressResult {
	jobMutex.Lock()
	defer jobMutex.Unlock()
	return jobProgress
}

// =============================================================================
// CancelCompression — request early cancellation
// =============================================================================

// CancelCompression signals the running compression goroutine to stop early.
// Returns {"status":"cancelled"}.
//
// FUTURE CONTRACT: call jobCancel() under jobMutex if a job is running; the
// pipeline observes ctx.Done() between images and unwinds cleanly.
func (a *App) CancelCompression() map[string]string {
	jobMutex.Lock()
	if jobCancel != nil {
		jobCancel()
	}
	jobMutex.Unlock()
	return map[string]string{"status": "cancelled"}
}

// =============================================================================
// GetImagePreview — base64 thumbnail of a single media part
// =============================================================================

// GetImagePreview returns a base64-encoded thumbnail (no data: prefix) of the
// media part named partName, so the frontend can preview an image on demand.
// Returns an empty string if the part cannot be decoded.
//
// JS usage:
//
//	const b64 = await App.GetImagePreview(partName);
//	if (b64) img.src = "data:image/png;base64," + b64;
//
// It reads the part from the cached analysed package, decodes it, downscales it
// to a small thumbnail, and returns a base64-encoded JPEG (no data: prefix).
func (a *App) GetImagePreview(partName string) string {
	a.mu.Lock()
	p := a.analyzed
	a.mu.Unlock()
	if p == nil {
		return ""
	}

	e := p.entry(partName)
	if e == nil || !isRasterFormat(detectFormat(e.data)) {
		return "" // only raster parts have previews
	}

	b64, err := thumbnailBase64(e.data, 160)
	if err != nil {
		return ""
	}
	return b64
}

// =============================================================================
// OpenOutputFolder — reveal the result in the OS file manager
// =============================================================================

// OpenOutputFolder reveals path in the OS file manager (Explorer on Windows,
// Finder on macOS). Called after a successful compression so the user can find
// the <name>_compressed.pptx.
//
// It asks the OS to reveal the file (or its containing folder) in the native
// file manager via the Wails browser-open runtime.
func (a *App) OpenOutputFolder(path string) error {
	if path == "" {
		return fmt.Errorf("no path to open")
	}
	// Open the containing directory. wailsRuntime.BrowserOpenURL hands the path
	// to the OS, which opens it in the default file manager.
	dir := path
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		dir = path[:i]
	}
	wailsRuntime.BrowserOpenURL(a.ctx, dir)
	return nil
}
