// =============================================================================
// types.go — Shared types and global state for the pptx-compressor backend.
// =============================================================================
//
// These types define the API contract between the Go backend and the JS
// frontend. Wails serialises them to/from JavaScript objects using the
// `json:` struct tags below, so the field names on the JS side match the tags
// here (camelCase). Fixing these types on day one keeps the Go ↔ JS bridge
// stable while the business logic is filled in later.
//
// The domain in one sentence: a .pptx is a ZIP archive; its images live under
// ppt/media/; we analyse them, recompress the ones that shrink, and rewrite a
// new .pptx next to the original.
// =============================================================================

package main

import (
	"context"
	"sync"
)

// =============================================================================
// Global background-job state (StartX / GetProgress / Cancel polling model)
// =============================================================================
//
// Long-running compression runs in a background goroutine. The frontend polls
// GetProgress() on a timer to read progress and, when finished, the results.
// All access to the shared progress value goes through jobMutex.

var (
	// jobMutex guards jobProgress and jobCancel below.
	jobMutex sync.Mutex

	// jobProgress holds the current state of the running (or last) compression.
	// GetProgress() returns a copy of this under the mutex.
	jobProgress ProgressResult

	// jobCancel cancels the currently running compression goroutine.
	// nil when no job is running.
	jobCancel context.CancelFunc
)

// =============================================================================
// MediaInfo — one row of the analysis table (a single image part)
// =============================================================================

// MediaInfo describes a single media part found under ppt/media/ inside the
// .pptx. One MediaInfo is produced per embedded image during analysis, and it
// carries both the observed facts (format, size, alpha) and the proposed plan
// (what we intend to do and how many bytes we expect to save).
type MediaInfo struct {
	// PartName is the ZIP path of the media part, e.g. "ppt/media/image3.png".
	PartName string `json:"partName"`

	// Format is the detected image format: "jpeg", "png", "gif", "emf", "wmf",
	// "svg", "webp", "tiff", etc. Vectors (emf/wmf/svg) and animated GIFs are
	// always skipped — see the domain rules in CLAUDE.md.
	Format string `json:"format"`

	// Width and Height are the pixel dimensions of the decoded image.
	// Zero for parts we cannot decode (e.g. vectors).
	Width  int `json:"width"`
	Height int `json:"height"`

	// Bytes is the current size of the part inside the ZIP (uncompressed image
	// bytes, i.e. the stored file length).
	Bytes int64 `json:"bytes"`

	// HasAlpha is true only when the image actually uses transparency. This
	// must be determined by sampling the full alpha channel, not by trusting
	// the format/metadata — an "RGBA" PNG can be fully opaque.
	HasAlpha bool `json:"hasAlpha"`

	// RefCount is how many times this part is referenced across slide, layout
	// and master .rels files. Zero means the part is unused and may be removed
	// (when the "remove unused media" option is enabled).
	RefCount int `json:"refCount"`

	// ProposedAction is the plan for this part, e.g. "recompress-jpeg",
	// "png->jpeg", "quantize", "downscale", "skip", "remove". Filled in by the
	// analyzer based on the active CompressionOptions.
	ProposedAction string `json:"proposedAction"`

	// EstimatedBytes is the predicted size after the proposed action. Used to
	// show estimated savings before the user commits to compression.
	EstimatedBytes int64 `json:"estimatedBytes"`
}

// =============================================================================
// AnalysisResult — the full inventory returned by AnalyzePptx
// =============================================================================

// AnalysisResult is the result of inspecting a .pptx without modifying it.
// It backs the analysis table and the "estimated savings" summary.
type AnalysisResult struct {
	// Path is the absolute path of the analysed .pptx.
	Path string `json:"path"`

	// Media is one entry per part under ppt/media/, in a stable order.
	Media []MediaInfo `json:"media"`

	// FileBytes is the size on disk of the whole .pptx (all parts + zip overhead).
	FileBytes int64 `json:"fileBytes"`

	// TotalBytes is the summed current size of all media parts.
	TotalBytes int64 `json:"totalBytes"`

	// EstimatedBytes is the summed predicted size of all media parts after
	// compression (the estimate; the real numbers come from the run).
	EstimatedBytes int64 `json:"estimatedBytes"`

	// UnusedCount is how many media parts have a reference count of zero.
	UnusedCount int `json:"unusedCount"`

	// HasEmbeddedFonts is true when the deck embeds fonts (ppt/fonts/*.fntdata),
	// which the "strip fonts" option can remove.
	HasEmbeddedFonts bool `json:"hasEmbeddedFonts"`

	// Error is a human-readable message if analysis failed; empty on success.
	Error string `json:"error"`
}

// =============================================================================
// CompressionOptions — user-chosen settings for a compression run
// =============================================================================

// CompressionOptions captures every knob the user can set in the options
// panel. It is embedded in CompressionRequest and read once at job start.
type CompressionOptions struct {
	// Preset is a coarse quality profile: "light", "balanced" or "aggressive".
	// It seeds sensible defaults for the finer-grained fields below.
	Preset string `json:"preset"`

	// JpegQuality is the target JPEG quality (1–100) for jpegli encoding.
	JpegQuality int `json:"jpegQuality"`

	// MaxEdgePx caps the longest edge in pixels; larger images are downscaled
	// with CatmullRom. Zero means "do not downscale".
	MaxEdgePx int `json:"maxEdgePx"`

	// MinSizeKB skips any image smaller than this threshold (in KB), since tiny
	// images rarely benefit from recompression.
	MinSizeKB int `json:"minSizeKB"`

	// ConvertOpaquePng converts fully-opaque PNGs to JPEG (usually much smaller).
	ConvertOpaquePng bool `json:"convertOpaquePng"`

	// QuantizeTransparentPng applies median-cut palette quantization to PNGs
	// that actually use transparency, keeping the alpha channel intact.
	QuantizeTransparentPng bool `json:"quantizeTransparentPng"`

	// UseWebp emits WebP output. Opt-in only: PowerPoint reads WebP solely on
	// Microsoft 365 version 2402 and later, so this stays off by default.
	UseWebp bool `json:"useWebp"`

	// RemoveUnusedMedia drops media parts with a reference count of zero.
	RemoveUnusedMedia bool `json:"removeUnusedMedia"`

	// StripEmbeddedFonts removes embedded font parts to save additional space.
	StripEmbeddedFonts bool `json:"stripEmbeddedFonts"`

	// PerImageOverrides maps a part name (e.g. "ppt/media/image3.png") to an
	// explicit action that overrides the global plan for that single image,
	// e.g. "skip" or "remove".
	PerImageOverrides map[string]string `json:"perImageOverrides"`
}

// =============================================================================
// CompressionRequest — the payload for StartCompression
// =============================================================================

// CompressionRequest is what the frontend sends to StartCompression: which
// file to compress and with which options.
type CompressionRequest struct {
	// Path is the absolute path of the source .pptx (never modified).
	Path string `json:"path"`

	// Options is the full set of user-chosen settings for this run.
	Options CompressionOptions `json:"options"`
}

// =============================================================================
// ProgressResult — the value returned by GetProgress
// =============================================================================

// ProgressResult is the polled status of a compression job. The frontend reads
// it every ~500ms to drive the progress bar and, on completion, the report.
type ProgressResult struct {
	// State is the lifecycle phase: "idle", "running", "done", "cancelled" or
	// "error".
	State string `json:"state"`

	// ProcessedCount and TotalCount drive the progress bar (images done / total).
	ProcessedCount int `json:"processedCount"`
	TotalCount     int `json:"totalCount"`

	// CurrentFile is the part name currently being processed (for display).
	CurrentFile string `json:"currentFile"`

	// BytesBefore and BytesAfter are the running totals for the before/after
	// report. BytesAfter is meaningful once State is "done".
	BytesBefore int64 `json:"bytesBefore"`
	BytesAfter  int64 `json:"bytesAfter"`

	// OutputPath is the path of the written <name>_compressed.pptx, set when
	// State becomes "done".
	OutputPath string `json:"outputPath"`

	// Errors collects per-image or fatal error messages encountered during the
	// run. A non-empty list does not necessarily mean the whole job failed.
	Errors []string `json:"errors"`
}
