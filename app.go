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

	// Wails runtime — used for native dialogs and OS integration once
	// implemented. Referenced here so the import contract is fixed early.
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application struct. All exported methods on *App are
// exposed to the frontend JavaScript as window.go.main.App.MethodName().
type App struct {
	ctx context.Context // Wails context; available after startup().
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
// FUTURE CONTRACT: call wailsRuntime.OpenFileDialog with a
// "PowerPoint (*.pptx)" file filter and return the selected path.
func (a *App) SelectPptxFile() (string, error) {
	// STUB: real implementation opens a native dialog. Return empty for now.
	_ = wailsRuntime.OpenDialogOptions{} // keep the runtime import wired in
	return "", nil
}

// =============================================================================
// AnalyzePptx — inventory the media parts (read-only)
// =============================================================================

// AnalyzePptx opens the .pptx at path (a ZIP archive) without modifying it,
// inventories every media part under ppt/media/, and returns an AnalysisResult
// describing each image and the estimated savings.
//
// FUTURE CONTRACT: open the archive, list ppt/media/ parts, decode each image
// to read format/dimensions/alpha, count references across the .rels graph, and
// compute a proposed action + estimated size per part. Never modifies the file.
func (a *App) AnalyzePptx(path string) AnalysisResult {
	// STUB: return an empty, non-error inventory so the table renders.
	return AnalysisResult{
		Path:  path,
		Media: []MediaInfo{},
	}
}

// =============================================================================
// StartCompression — launch the background compression job
// =============================================================================

// StartCompression launches compression for req in a background goroutine and
// returns immediately with {"status":"running"}. The frontend then polls
// GetProgress() until the state is "done", "cancelled" or "error".
//
// FUTURE CONTRACT: reset the shared job state, create a cancellable context,
// store its cancel func in jobCancel, and start the worker-pool pipeline
// (compressor.go). Writes <name>_compressed.pptx next to the source; the source
// is never touched.
func (a *App) StartCompression(req CompressionRequest) map[string]string {
	// STUB: mark the job idle and acknowledge. No work is started yet.
	jobMutex.Lock()
	jobProgress = ProgressResult{State: "idle"}
	jobMutex.Unlock()

	_ = req // req is unused until the pipeline is implemented.
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
// FUTURE CONTRACT: read the part from the currently-analysed archive, decode
// and downscale it to a small preview, and return the encoded bytes.
func (a *App) GetImagePreview(partName string) string {
	// STUB: no preview available yet.
	_ = partName
	return ""
}

// =============================================================================
// OpenOutputFolder — reveal the result in the OS file manager
// =============================================================================

// OpenOutputFolder reveals path in the OS file manager (Explorer on Windows,
// Finder on macOS). Called after a successful compression so the user can find
// the <name>_compressed.pptx.
//
// FUTURE CONTRACT: open the containing folder (and ideally select the file)
// using the platform file manager.
func (a *App) OpenOutputFolder(path string) error {
	// STUB: no-op until implemented.
	_ = path
	return nil
}
