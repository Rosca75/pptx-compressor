// api.js — Wails binding wrappers for all Go backend methods.
//
// This is the ONLY file that calls window.go.main.App.*
// All other modules import from here — never call Wails bindings directly.
//
// Wails makes each public method on *App available as:
//   window.go.main.App.MethodName(args...) → Promise
//
// The Go structs are serialised to/from JS objects using the `json:` tags
// defined in types.go, so the field names below are camelCase.

/** Helper: return the Wails App binding. */
const GoApp = () => window.go.main.App;

/**
 * Open the native file picker filtered to .pptx.
 * Wraps Go: SelectPptxFile() (string, error)
 * @returns {Promise<string>} chosen path, or "" if cancelled.
 */
export function apiSelectPptxFile() {
  return GoApp().SelectPptxFile();
}

/**
 * Inventory the media parts of a .pptx (read-only).
 * Wraps Go: AnalyzePptx(path string) AnalysisResult
 * @param {string} path - Absolute path of the .pptx.
 * @returns {Promise<Object>} AnalysisResult { path, media[], totalBytes, estimatedBytes, error }.
 */
export function apiAnalyzePptx(path) {
  return GoApp().AnalyzePptx(path || '');
}

/**
 * Start a background compression run.
 * Wraps Go: StartCompression(req CompressionRequest) map[string]string
 * Returns immediately; poll apiGetProgress() for status.
 * @param {Object} req - CompressionRequest { path, options }.
 * @returns {Promise<Object>} { status, message }.
 */
export function apiStartCompression(req) {
  return GoApp().StartCompression(req);
}

/**
 * Poll the current compression job status.
 * Wraps Go: GetProgress() ProgressResult
 * @returns {Promise<Object>} ProgressResult { state, processedCount, totalCount, currentFile, bytesBefore, bytesAfter, outputPath, errors }.
 */
export function apiGetProgress() {
  return GoApp().GetProgress();
}

/**
 * Request cancellation of the running compression job.
 * Wraps Go: CancelCompression() map[string]string
 * @returns {Promise<Object>} { status }.
 */
export function apiCancelCompression() {
  return GoApp().CancelCompression();
}

/**
 * Fetch a base64 thumbnail of a single media part.
 * Wraps Go: GetImagePreview(partName string) string
 * Usage: img.src = "data:image/png;base64," + await apiGetImagePreview(name);
 * @param {string} partName - e.g. "ppt/media/image3.png".
 * @returns {Promise<string>} base64 string, or "" if unavailable.
 */
export function apiGetImagePreview(partName) {
  return GoApp().GetImagePreview(partName || '');
}

/**
 * Reveal a produced file in the OS file manager.
 * Wraps Go: OpenOutputFolder(path string) error
 * @param {string} path - Path of the output .pptx.
 * @returns {Promise<void>}
 */
export function apiOpenOutputFolder(path) {
  return GoApp().OpenOutputFolder(path || '');
}
