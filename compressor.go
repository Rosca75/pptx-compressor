// =============================================================================
// compressor.go — Worker-pool compression pipeline.
// =============================================================================
//
// This file drives an actual compression run. App.StartCompression launches
// runCompression as a background goroutine; it reports progress through the
// shared jobProgress value (guarded by jobMutex) that GetProgress() returns.
//
// PIPELINE SHAPE (see BUILD.md §Phase 4):
//
//   [feeder] --media--> [worker pool] --results--> [coordinator]
//
//   - The feeder sends each media part down a jobs channel, stopping early if
//     the cancellation context fires.
//   - Workers (one per CPU) are READ-ONLY on the package: each decodes,
//     downscales and re-encodes its image and proposes new bytes / a new name.
//     They never mutate the PptxFile, so no locking of the package is needed.
//   - A single coordinator applies every result to the PptxFile in sequence
//     (renames rewrite .rels and content types and are NOT safe to run
//     concurrently — doing them on one goroutine keeps them correct).
//
// GOLDEN RULES enforced here:
//   - Never enlarge: if re-encoding is not smaller, the ORIGINAL bytes are kept
//     and the action is reported as "kept".
//   - Never modify the source: output is a new <name>_compressed.pptx.
//   - After writing, the output is re-opened and every relationship is verified
//     to resolve; if not, the output is discarded and the job fails loudly.
// =============================================================================

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	xbmp "golang.org/x/image/bmp"
	xtiff "golang.org/x/image/tiff"
)

// workResult is one worker's proposal for a single media part.
type workResult struct {
	oldName string // the part's current name in the package
	newName string // the proposed name (== oldName when the extension is unchanged)
	newData []byte // proposed bytes; nil means "keep the original untouched"
	ctype   string // content type for newName when it is a rename
	action  string // act* label for the report
	before  int64  // original size
	err     error  // per-image error (non-fatal; the original is kept)
}

// =============================================================================
// runCompression — the whole job
// =============================================================================

// runCompression executes a full compression job for req and stores the outcome
// in the shared jobProgress. It is meant to run in its own goroutine (spawned by
// StartCompression). ctx cancellation stops the run between images.
func runCompression(ctx context.Context, req CompressionRequest) {
	opts := resolveOptions(req.Options)

	// Open a FRESH copy of the source (never the cached analysis) so a run is
	// independent of any prior analysis and the source stays untouched.
	p, err := OpenPptx(req.Path)
	if err != nil {
		finishError(fmt.Sprintf("open: %v", err))
		return
	}
	if err := p.BuildRelsIndex(); err != nil {
		finishError(fmt.Sprintf("rels: %v", err))
		return
	}

	// Inventory media parts (format/dimensions/alpha/refCount) using the run's
	// options so DecideAction sees the same facts the analyzer showed the user.
	media := AnalyzeMedia(p, opts)

	// Initialise progress.
	jobMutex.Lock()
	jobProgress = ProgressResult{
		State:      "running",
		TotalCount: len(media),
		Results:    make([]ImageResult, 0, len(media)),
	}
	jobMutex.Unlock()

	// ---- worker pool ----------------------------------------------------
	numWorkers := runtime.NumCPU()
	if numWorkers < 1 {
		numWorkers = 1
	}
	jobs := make(chan MediaInfo)
	results := make(chan workResult)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				// Cheap cancellation check between images.
				if ctx.Err() != nil {
					return
				}
				e := p.entry(m.PartName)
				if e == nil {
					continue
				}
				results <- transformPart(ctx, e, m, opts)
			}
		}()
	}

	// Feeder: push media parts until the work is done or cancelled.
	go func() {
		defer close(jobs)
		for _, m := range media {
			select {
			case <-ctx.Done():
				return
			case jobs <- m:
			}
		}
	}()

	// Close results once all workers have finished.
	go func() {
		wg.Wait()
		close(results)
	}()

	// ---- coordinator ----------------------------------------------------
	// Apply results to the package one at a time and update progress.
	for r := range results {
		applyResult(p, r)
	}

	// If we were cancelled, stop here without writing an output.
	if ctx.Err() != nil {
		jobMutex.Lock()
		jobProgress.State = "cancelled"
		jobMutex.Unlock()
		return
	}

	// ---- post-processing (behind option flags) --------------------------
	if opts.RemoveUnusedMedia {
		removeUnusedMedia(p)
	}
	if opts.StripEmbeddedFonts || len(opts.RemoveFontTypefaces) > 0 {
		stripEmbeddedFonts(p, opts.RemoveFontTypefaces, opts.StripEmbeddedFonts)
	}

	// ---- write output + safety check ------------------------------------
	//
	// We ALWAYS write to the "<name>_compressed.pptx" staging path first and
	// verify it, even when the user asked to replace the original. Only once we
	// have a proven-good file do we optionally move it over the source. This
	// guarantees a failed or structurally-broken run never damages the original
	// (domain rule: never modify the source until a verified output exists).
	srcSizeBefore := fileSize(req.Path) // captured before any replace
	stagingPath := compressedOutputPath(req.Path)
	if err := p.WritePptx(stagingPath, true); err != nil {
		finishError(fmt.Sprintf("write output: %v", err))
		return
	}

	// Re-open the written file and verify every relationship resolves. If the
	// output is structurally broken we discard it rather than hand the user a
	// file PowerPoint would refuse to open.
	if err := verifyOutput(stagingPath); err != nil {
		removeFile(stagingPath)
		finishError(fmt.Sprintf("output failed safety check: %v", err))
		return
	}

	// Decide the final destination. When ReplaceOriginal is set, move the
	// verified staging file over the source (os.Rename replaces the existing
	// file atomically on both Windows and Unix). Otherwise the staging file IS
	// the deliverable.
	outPath := stagingPath
	if opts.ReplaceOriginal {
		if err := os.Rename(stagingPath, req.Path); err != nil {
			removeFile(stagingPath)
			finishError(fmt.Sprintf("replace original: %v", err))
			return
		}
		outPath = req.Path
	}

	// Success — record final whole-file sizes.
	jobMutex.Lock()
	jobProgress.State = "done"
	jobProgress.OutputPath = outPath
	jobProgress.FileBytesBefore = srcSizeBefore
	jobProgress.FileBytesAfter = fileSize(outPath)
	jobMutex.Unlock()
}

// =============================================================================
// transformPart — the per-image work done by a worker (read-only on package)
// =============================================================================

// transformPart decides and performs the re-encode for a single media part and
// returns a proposal. It reads e.data but never mutates the package. The
// never-larger guarantee is applied here: if the re-encoded bytes are not
// smaller, the proposal keeps the original (action "kept"). ctx lets a Cancel
// press kill a long-running ffmpeg video encode mid-file.
func transformPart(ctx context.Context, e *zipEntry, m MediaInfo, opts CompressionOptions) workResult {
	res := workResult{oldName: m.PartName, newName: m.PartName, before: int64(len(e.data))}

	act := DecideAction(m, opts)

	switch act.Kind {
	case actSkip:
		res.action = actSkip
		return res

	case actRemoveVideo:
		// "Remove videos": swap the bytes for the tiny placeholder MP4. The
		// part name, relationships and content type all stay intact, so the
		// package cannot break — the slide keeps its (now inert) poster frame.
		ph := placeholderMP4()
		if int64(len(ph)) >= res.before {
			res.action = actSkip // already tiny — nothing to gain
			return res
		}
		res.newData = ph
		res.action = actRemoveVideo
		return res

	case actCompressVideo:
		// MP4 re-encode through the external ffmpeg executable. Any failure
		// (no ffmpeg, broken file, cancelled) keeps the original bytes.
		out, err := compressVideoMP4(ctx, e.data, act.VideoLevel)
		if err != nil {
			res.err = fmt.Errorf("video %s: %w", m.PartName, err)
			res.action = actSkip
			return res
		}
		if int64(len(out)) >= res.before {
			res.action = actKept // never-larger guarantee
			return res
		}
		res.newData = out
		res.action = actCompressVideo
		return res

	case actRemove:
		// Per-image "remove": neutralise to a 1×1 placeholder in the SAME format
		// (keyed on the part's extension) so the relationship and content type
		// stay valid. Never delete a referenced part.
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(m.PartName)), ".")
		ph, ok := tinyPlaceholder(ext)
		if !ok {
			res.action = actSkip // unknown extension — leave it alone
			return res
		}
		res.newData = ph
		res.action = actRemove
		return res
	}

	// Animated GIFs are never re-encoded (would drop frames).
	if m.Format == fmtGIF && isAnimatedGIF(e.data) {
		res.action = actSkip
		return res
	}

	// Decode the source image.
	img, _, err := decodeImage(e.data)
	if err != nil {
		res.err = fmt.Errorf("decode %s: %w", m.PartName, err)
		res.action = actSkip
		return res
	}

	// Downscale first (never upscales).
	img, _ = resizeToMaxEdge(img, act.MaxEdge)

	// Encode along the chosen path.
	var out []byte
	switch act.Kind {
	case actRecompressJPEG, actPngToJpeg:
		out, err = encodeJPEG(flattenOntoWhite(img), act.Quality)
	case actWebp:
		out, err = encodeWebP(img, act.Quality)
	case actQuantizePng:
		out, err = quantizePNG(img, 256)
	case actRecompressPng:
		out, err = encodePNG(img)
	default:
		res.action = actSkip
		return res
	}
	if err != nil {
		res.err = fmt.Errorf("encode %s: %w", m.PartName, err)
		res.action = actSkip
		return res
	}

	// Never-larger guarantee: keep the original if the re-encode did not shrink.
	if len(out) >= len(e.data) {
		res.action = actKept
		return res
	}

	// Accepted. Compute the new name if the format (extension) changed.
	res.newData = out
	res.action = act.Kind
	res.ctype = act.ContentType
	if act.NewExt != "" {
		res.newName = replaceExt(m.PartName, act.NewExt)
	}
	return res
}

// =============================================================================
// applyResult — coordinator side; mutates the package for one result
// =============================================================================

// applyResult applies a worker's proposal to the package and updates progress.
// Runs on a single goroutine, so package mutations (data swap, rename, rels
// rewrite) are serialised and safe.
func applyResult(p *PptxFile, r workResult) {
	after := r.before // default: unchanged

	if r.newData != nil {
		// Swap the bytes in place first, then rename if the extension changed.
		if err := p.ReplacePartData(r.oldName, r.newData); err == nil {
			after = int64(len(r.newData))
			if r.newName != r.oldName {
				if rerr := p.RenameMediaPart(r.oldName, r.newName, r.ctype); rerr != nil {
					// Rename failed after a data swap — record it; the part still
					// holds valid (new-format) bytes under the old name, which for
					// JPEG-in-a-.png part is tolerated, but flag it for the report.
					r.err = fmt.Errorf("rename %s: %w", r.oldName, rerr)
				}
			}
		} else {
			r.err = err
			after = r.before
		}
	}

	jobMutex.Lock()
	jobProgress.ProcessedCount++
	jobProgress.CurrentFile = r.oldName
	jobProgress.BytesBefore += r.before
	jobProgress.BytesAfter += after
	jobProgress.Results = append(jobProgress.Results, ImageResult{
		PartName:    r.oldName,
		Action:      r.action,
		BeforeBytes: r.before,
		AfterBytes:  after,
	})
	if r.err != nil {
		jobProgress.Errors = append(jobProgress.Errors, r.err.Error())
	}
	jobMutex.Unlock()
}

// =============================================================================
// Post-processing steps
// =============================================================================

// removeUnusedMedia deletes every media part whose reference count is zero.
// PowerPoint often leaves orphaned media behind; dropping them is pure gain.
func removeUnusedMedia(p *PptxFile) {
	// Rebuild the index because renames above changed part names.
	if err := p.BuildRelsIndex(); err != nil {
		return
	}
	for _, name := range p.MediaParts() {
		if p.RefCount(name) == 0 {
			_ = p.RemoveMediaPart(name) // refCount==0, cannot dangle
		}
	}
}

// fontRelType matches the relationship type used for embedded fonts.
const fontRelType = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/font"

// embeddedFontLstRe matches the <p:embeddedFontLst>...</p:embeddedFontLst>
// element (with any namespace prefix) inside ppt/presentation.xml.
var embeddedFontLstRe = regexp.MustCompile(`(?s)<([a-zA-Z0-9]+:)?embeddedFontLst\b.*?</([a-zA-Z0-9]+:)?embeddedFontLst>`)

// stripEmbeddedFonts removes embedded fonts from the package. Fonts are stored
// uncompressed and are essentially incompressible, so removal is the only way to
// reclaim their space; recipients without the fonts installed then see a
// substitute font (the UI warns about this).
//
// Two modes:
//   - all == true: remove EVERY embedded font (the coarse "strip all" switch).
//   - otherwise: remove only the families named in typefaces, leaving the rest
//     embedded. This is what the per-font Fonts tab uses so a user can drop a
//     huge fallback face (e.g. Arial Unicode MS) while keeping small brand fonts.
//
// In both modes it performs the three edits an embedded-font removal requires:
// deleting the ppt/fonts/*.fntdata parts, dropping their relationships in
// ppt/_rels/presentation.xml.rels, and removing the matching markup in
// ppt/presentation.xml (the whole <embeddedFontLst>, or the individual
// <embeddedFont> elements).
func stripEmbeddedFonts(p *PptxFile, typefaces []string, all bool) {
	pres := p.entry("ppt/presentation.xml")
	if pres == nil {
		return
	}
	rels := p.entry("ppt/_rels/presentation.xml.rels")

	if all {
		stripAllEmbeddedFonts(p, pres, rels)
		return
	}
	if len(typefaces) == 0 {
		return // nothing selected
	}

	// Which families should go?
	want := make(map[string]bool, len(typefaces))
	for _, t := range typefaces {
		want[t] = true
	}

	// Reuse the analyzer's inventory so "which parts/rels back this family" has a
	// single definition. Walk it, removing the selected families' markup, rels
	// and parts and counting how many families remain embedded.
	fontParts := map[string]bool{}
	remaining := 0
	for _, f := range embeddedFonts(p) {
		if !want[f.Typeface] {
			remaining++
			continue
		}
		// Remove this family's <embeddedFont> element from presentation.xml.
		pres.data = removeEmbeddedFontElement(pres.data, f.Typeface)
		// Drop its relationships and remember its parts for deletion.
		for _, part := range f.PartNames {
			fontParts[part] = true
		}
		if rels != nil {
			for _, id := range f.RelIDs {
				rels.data = removeRelationshipElement(rels.data, id)
			}
		}
	}

	// If nothing is left embedded, drop the now-empty <embeddedFontLst> wrapper.
	if remaining == 0 {
		pres.data = embeddedFontLstRe.ReplaceAll(pres.data, nil)
	}

	deleteFontParts(p, fontParts)
}

// stripAllEmbeddedFonts removes every embedded font in one pass: the whole
// <embeddedFontLst>, all font relationships, and all ppt/fonts/*.fntdata parts.
func stripAllEmbeddedFonts(p *PptxFile, pres, rels *zipEntry) {
	// 1) Remove the embeddedFontLst element from presentation.xml.
	pres.data = embeddedFontLstRe.ReplaceAll(pres.data, nil)

	// 2) Remove font relationships from presentation.xml.rels and collect the
	//    font part names they targeted so we can delete those parts.
	fontParts := map[string]bool{}
	if rels != nil {
		var rf relsFile
		if err := xml.Unmarshal(rels.data, &rf); err == nil {
			base := relsBaseDir(rels.name) // "ppt"
			kept := rels.data
			for _, rel := range rf.Relationships {
				if rel.Type == fontRelType {
					fontParts[resolveTarget(base, rel.Target)] = true
					kept = removeRelationshipElement(kept, rel.Id)
				}
			}
			rels.data = kept
		}
	}

	// 3) Delete the font parts themselves. Every ppt/fonts/*.fntdata goes, plus
	//    any target collected above (belt and braces).
	for i := 0; i < len(p.Entries); {
		e := p.Entries[i]
		isFontData := strings.HasPrefix(e.name, "ppt/fonts/") && strings.HasSuffix(e.name, ".fntdata")
		if isFontData || fontParts[e.name] {
			p.Entries = append(p.Entries[:i], p.Entries[i+1:]...)
			continue
		}
		i++
	}
}

// deleteFontParts removes the named entries from the package. Font parts live
// outside ppt/media/, so they are removed directly (RemoveMediaPart's refcount
// guard and ppt/media/ scope do not apply).
func deleteFontParts(p *PptxFile, parts map[string]bool) {
	if len(parts) == 0 {
		return
	}
	for i := 0; i < len(p.Entries); {
		if parts[p.Entries[i].name] {
			p.Entries = append(p.Entries[:i], p.Entries[i+1:]...)
			continue
		}
		i++
	}
}

// removeRelationshipElement removes the single <Relationship ... Id="id" .../>
// element from a .rels document by locating the element that contains that Id.
func removeRelationshipElement(data []byte, id string) []byte {
	needle := []byte(`Id="` + id + `"`)
	idx := bytes.Index(data, needle)
	if idx < 0 {
		return data
	}
	// Find the "<" that opens this element and the ">" that closes it.
	start := bytes.LastIndex(data[:idx], []byte("<"))
	if start < 0 {
		return data
	}
	end := bytes.Index(data[idx:], []byte(">"))
	if end < 0 {
		return data
	}
	end = idx + end + 1
	return append(data[:start:start], data[end:]...)
}

// removeEmbeddedFontElement removes the single <p:embeddedFont>...</p:embeddedFont>
// element whose child <p:font> declares the given typeface, from a
// presentation.xml document. Namespace prefixes are optional and the typeface is
// regex-escaped, so an exact family-name match is required. If nothing matches
// (or the pattern fails to compile) the input is returned unchanged.
func removeEmbeddedFontElement(data []byte, typeface string) []byte {
	pat := `(?s)<([a-zA-Z0-9]+:)?embeddedFont\b[^>]*>\s*<([a-zA-Z0-9]+:)?font\b[^>]*\btypeface="` +
		regexp.QuoteMeta(typeface) + `"[^>]*>.*?</([a-zA-Z0-9]+:)?embeddedFont>`
	re, err := regexp.Compile(pat)
	if err != nil {
		return data
	}
	return re.ReplaceAll(data, nil)
}

// =============================================================================
// Helpers
// =============================================================================

// replaceExt returns name with its extension replaced by newExt (no dot).
// "ppt/media/image3.png" + "jpeg" -> "ppt/media/image3.jpeg".
func replaceExt(name, newExt string) string {
	ext := path.Ext(name)
	base := name[:len(name)-len(ext)]
	return base + "." + newExt
}

// compressedOutputPath returns "<dir>/<name>_compressed.pptx" for the source,
// using OS-native path handling so it is correct on Windows and Unix alike.
func compressedOutputPath(src string) string {
	dir := filepath.Dir(src)
	baseWithExt := filepath.Base(src)
	ext := filepath.Ext(baseWithExt)
	base := baseWithExt[:len(baseWithExt)-len(ext)]
	return filepath.Join(dir, base+"_compressed"+ext)
}

// finishError records a fatal job failure in the shared progress.
func finishError(msg string) {
	jobMutex.Lock()
	jobProgress.State = "error"
	jobProgress.Errors = append(jobProgress.Errors, msg)
	jobMutex.Unlock()
}

// verifyOutput re-opens a written .pptx and checks every media relationship
// still resolves to an existing part — the release-gate safety check.
func verifyOutput(outPath string) error {
	out, err := OpenPptx(outPath)
	if err != nil {
		return err
	}
	return out.VerifyRelationships()
}

// removeFile deletes a file, ignoring errors (best-effort cleanup).
func removeFile(pathName string) { _ = os.Remove(pathName) }

// tinyPlaceholder returns a minimal 1×1 image encoded in the format matching
// ext, used by the per-image "remove" override. The boolean is false for
// extensions we cannot safely synthesise (the caller then leaves the part
// untouched rather than risk an invalid content type).
func tinyPlaceholder(ext string) ([]byte, bool) {
	// A 1×1 fully-transparent (or white) pixel.
	transparent := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	transparent.Set(0, 0, color.NRGBA{})
	white := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	white.Set(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 255})

	var buf bytes.Buffer
	switch ext {
	case "png":
		if b, err := encodePNG(transparent); err == nil {
			return b, true
		}
	case "jpg", "jpeg":
		if b, err := encodeJPEG(white, 50); err == nil {
			return b, true
		}
	case "webp":
		if b, err := encodeWebP(transparent, 50); err == nil {
			return b, true
		}
	case "gif":
		pal := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Transparent, color.White})
		if err := gif.Encode(&buf, pal, nil); err == nil {
			return buf.Bytes(), true
		}
	case "bmp":
		if err := xbmp.Encode(&buf, white); err == nil {
			return buf.Bytes(), true
		}
	case "tif", "tiff":
		if err := xtiff.Encode(&buf, transparent, nil); err == nil {
			return buf.Bytes(), true
		}
	case "mp4", "m4v", "mov":
		// MP4-family videos: the embedded ~2 KB placeholder video (also used
		// by the global "remove videos" option). PowerPoint stores .mov parts
		// in the same ISO container family, so the bytes remain valid there.
		return placeholderMP4(), true
	}
	return nil, false
}
