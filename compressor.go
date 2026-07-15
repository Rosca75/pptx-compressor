// =============================================================================
// compressor.go — Worker-pool compression pipeline.
// =============================================================================
//
// This file drives an actual compression run. It is launched as a background
// goroutine by App.StartCompression and reports progress through the shared
// jobProgress value (guarded by jobMutex) that GetProgress() returns.
//
// FUTURE RESPONSIBILITIES (implemented in a later session):
//   - Read the analysis / options and build the work list of media parts.
//   - Fan the work out across a worker pool (one worker per CPU by default),
//     re-encoding each eligible image via codec.go and downscaling via resize.go.
//   - Honour the cancellation context between images so CancelCompression()
//     unwinds promptly.
//   - Enforce the golden rule: keep the re-encoded bytes ONLY if they are
//     smaller than the original; if re-encoding produces a larger (or equal)
//     result, keep the original bytes untouched.
//   - Accumulate before/after byte totals and per-image errors into
//     jobProgress, then hand the rewritten part set to pptx.go for re-zipping.
//
// This file currently contains no implementation — only this contract.
// =============================================================================

package main
