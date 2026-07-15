// =============================================================================
// pptx.go — PPTX archive I/O: ZIP open/rewrite, content types, .rels graph.
// =============================================================================
//
// A .pptx file is an Office Open XML (OOXML) package: a ZIP archive containing
// XML parts plus binary media. The parts this file manages:
//
//   [Content_Types].xml   Declares the MIME type of every part, via extension
//                         defaults (<Default Extension="png" .../>) and
//                         per-part overrides (<Override PartName="..." .../>).
//   _rels/.rels           Package-level relationships (entry points).
//   ppt/_rels/*.rels      Relationships from the presentation to its slides.
//   ppt/slides/.../*.rels Per-slide/layout/master relationships that point at
//                         media parts under ppt/media/.
//   ppt/media/*           The embedded images we recompress.
//
// This file provides an in-memory model of the package (PptxFile) plus the
// operations the compression pipeline needs:
//
//   - OpenPptx:        read every entry into memory and validate the package.
//   - BuildRelsIndex:  map each media part to every relationship that points at
//                      it, so its reference count is known.
//   - RenameMediaPart: change a part's extension ATOMICALLY across the three
//                      places that must agree (the zip entry, [Content_Types].xml,
//                      and every referencing .rels file), or PowerPoint refuses
//                      to open the result.
//   - RemoveMediaPart: drop an unreferenced part.
//   - WritePptx:       re-zip to <name>_compressed.pptx; the source is untouched.
//
// DESIGN NOTE — everything is held in memory. A .pptx is read fully into a byte
// slice per entry. Real-world decks are well under ~500 MB, so this keeps the
// code simple and the rename/rewrite logic straightforward. If multi-gigabyte
// decks ever appear, this is the assumption to revisit.
// =============================================================================

package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// mediaPrefix is the folder inside the package where embedded images live.
const mediaPrefix = "ppt/media/"

// contentTypesPart is the well-known name of the content-types manifest.
const contentTypesPart = "[Content_Types].xml"

// =============================================================================
// zipEntry — one file inside the .pptx ZIP archive
// =============================================================================

// zipEntry is a single member of the ZIP archive, held fully in memory.
// The order of entries is preserved from the original archive and written back
// in the same order, because some OOXML consumers are sensitive to part order
// (notably [Content_Types].xml being first).
type zipEntry struct {
	// name is the archive path, e.g. "ppt/media/image3.png" or "[Content_Types].xml".
	name string

	// data is the uncompressed bytes of this part.
	data []byte

	// method is the ZIP compression method the original archive used for this
	// entry (zip.Store or zip.Deflate). Preserving it means already-compressed
	// media (usually Stored) is not needlessly re-deflated, while XML parts
	// (usually Deflated) are re-compressed at the best level on write.
	method uint16
}

// =============================================================================
// relRef — one relationship pointing at a media part
// =============================================================================

// relRef records a single <Relationship> element (inside some .rels file) that
// targets a media part. We keep enough context to rewrite the target in place
// when the part is renamed.
type relRef struct {
	// relsPart is the archive name of the .rels file that contains this
	// relationship, e.g. "ppt/slides/_rels/slide1.xml.rels".
	relsPart string

	// id is the relationship Id attribute (e.g. "rId2"). Not strictly needed for
	// renaming but useful for diagnostics.
	id string

	// target is the raw Target attribute value exactly as it appears in the XML,
	// e.g. "../media/image3.png". Relative to the owning part's folder.
	target string
}

// =============================================================================
// PptxFile — the in-memory model of an opened .pptx package
// =============================================================================

// PptxFile is a fully in-memory model of a .pptx package: every entry, the
// parsed content-types manifest, and the media→relationships index.
type PptxFile struct {
	// SourcePath is the absolute path the package was opened from (never written).
	SourcePath string

	// Entries holds every ZIP member in original order. Renames mutate the name
	// field in place, so order is preserved across a rename.
	Entries []*zipEntry

	// ctDefaults maps a lower-cased file extension (without dot) to its declared
	// content type, e.g. "png" -> "image/png". Parsed from [Content_Types].xml.
	ctDefaults map[string]string

	// ctOverrides maps a full part name to a per-part content type override,
	// e.g. "/ppt/slides/slide1.xml" -> "application/vnd...+xml".
	ctOverrides map[string]string

	// relsIndex maps a media part name (e.g. "ppt/media/image3.png") to every
	// relationship that references it. Built by BuildRelsIndex().
	relsIndex map[string][]relRef
}

// =============================================================================
// OpenPptx — read and validate a .pptx package into memory
// =============================================================================

// OpenPptx reads the .pptx at path fully into memory and validates that it is a
// PowerPoint package. It does not modify the file on disk.
//
// Validation: the archive must contain both [Content_Types].xml and
// ppt/presentation.xml, which every real .pptx has. A ZIP missing either is
// almost certainly not a presentation (or is corrupt), so we reject it early
// rather than producing a broken output later.
func OpenPptx(path string) (*PptxFile, error) {
	// zip.OpenReader memory-maps the archive directory and lets us stream each
	// entry. We copy every entry's bytes out so the reader can be closed and the
	// whole package lives in RAM (see the design note at the top of the file).
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open pptx %q: %w", path, err)
	}
	defer zr.Close()

	p := &PptxFile{
		SourcePath:  path,
		Entries:     make([]*zipEntry, 0, len(zr.File)),
		ctDefaults:  map[string]string{},
		ctOverrides: map[string]string{},
		relsIndex:   map[string][]relRef{},
	}

	// Read each ZIP member's bytes into memory, preserving archive order.
	for _, f := range zr.File {
		// Skip directory entries (names ending in "/"); OOXML packages rarely
		// contain them, and they carry no data.
		if f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read entry %q: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read entry %q: %w", f.Name, err)
		}

		p.Entries = append(p.Entries, &zipEntry{
			name:   f.Name,
			data:   data,
			method: f.Method,
		})
	}

	// Validate: this must look like a PowerPoint package.
	if p.entry(contentTypesPart) == nil {
		return nil, fmt.Errorf("not a valid pptx: missing %s", contentTypesPart)
	}
	if p.entry("ppt/presentation.xml") == nil {
		return nil, fmt.Errorf("not a valid pptx: missing ppt/presentation.xml")
	}

	// Parse the content-types manifest so EnsureDefault can query and extend it.
	if err := p.parseContentTypes(); err != nil {
		return nil, err
	}

	return p, nil
}

// entry returns the zipEntry with the given name, or nil if absent.
func (p *PptxFile) entry(name string) *zipEntry {
	for _, e := range p.Entries {
		if e.name == name {
			return e
		}
	}
	return nil
}

// MediaParts returns the names of every entry under ppt/media/, in archive
// order. These are the candidate images for compression.
func (p *PptxFile) MediaParts() []string {
	var out []string
	for _, e := range p.Entries {
		if strings.HasPrefix(e.name, mediaPrefix) {
			out = append(out, e.name)
		}
	}
	return out
}

// =============================================================================
// Content types — parse and extend [Content_Types].xml
// =============================================================================

// ctTypes mirrors the structure of [Content_Types].xml just enough to read the
// Default (extension) and Override (per-part) declarations with encoding/xml.
type ctTypes struct {
	XMLName  xml.Name     `xml:"Types"`
	Defaults []ctDefault  `xml:"Default"`
	Override []ctOverride `xml:"Override"`
}

type ctDefault struct {
	Extension   string `xml:"Extension,attr"`
	ContentType string `xml:"ContentType,attr"`
}

type ctOverride struct {
	PartName    string `xml:"PartName,attr"`
	ContentType string `xml:"ContentType,attr"`
}

// parseContentTypes reads [Content_Types].xml into the ctDefaults / ctOverrides
// maps. Extensions are stored lower-cased so lookups are case-insensitive.
func (p *PptxFile) parseContentTypes() error {
	e := p.entry(contentTypesPart)
	if e == nil {
		return fmt.Errorf("missing %s", contentTypesPart)
	}

	var t ctTypes
	if err := xml.Unmarshal(e.data, &t); err != nil {
		return fmt.Errorf("parse %s: %w", contentTypesPart, err)
	}

	for _, d := range t.Defaults {
		p.ctDefaults[strings.ToLower(d.Extension)] = d.ContentType
	}
	for _, o := range t.Override {
		p.ctOverrides[o.PartName] = o.ContentType
	}
	return nil
}

// EnsureDefault guarantees that [Content_Types].xml declares a Default content
// type for the given extension. If the extension is already declared it is a
// no-op; otherwise a <Default> element is inserted just before </Types>,
// preserving the rest of the manifest byte-for-byte.
//
// This is required whenever a media part changes to an extension the package
// did not previously use (e.g. the first PNG→JPEG conversion introduces "jpeg").
// Without a declared content type PowerPoint refuses to open the file.
func (p *PptxFile) EnsureDefault(ext, contentType string) {
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if _, ok := p.ctDefaults[ext]; ok {
		return // already declared — nothing to do
	}

	e := p.entry(contentTypesPart)
	if e == nil {
		return // OpenPptx guarantees this exists, but stay defensive
	}

	// Build the new element and splice it in immediately before the closing
	// </Types> tag. String insertion keeps the original formatting intact.
	elem := fmt.Sprintf(`<Default Extension="%s" ContentType="%s"/>`, ext, contentType)

	xmlStr := string(e.data)
	idx := strings.LastIndex(xmlStr, "</Types>")
	if idx < 0 {
		return // malformed manifest; leave it alone rather than corrupt it
	}
	e.data = []byte(xmlStr[:idx] + elem + xmlStr[idx:])

	// Record it so a subsequent EnsureDefault for the same extension is a no-op.
	p.ctDefaults[ext] = contentType
}

// =============================================================================
// Rels graph — index which relationships reference each media part
// =============================================================================

// relsFile mirrors a .rels file for parsing with encoding/xml.
type relsFile struct {
	XMLName       xml.Name          `xml:"Relationships"`
	Relationships []relRelationship `xml:"Relationship"`
}

type relRelationship struct {
	Id     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

// BuildRelsIndex parses every *.rels file in the package and builds relsIndex:
// a map from each referenced media part name to the list of relationships that
// point at it. A media part's reference count is len(relsIndex[partName]).
//
// Relationship targets are stored relative to the folder of the part that owns
// the .rels file. For a .rels named "<dir>/_rels/<file>.rels", the owning part
// is "<dir>/<file>", so targets resolve against "<dir>". Example: a target of
// "../media/image1.png" inside "ppt/slides/_rels/slide1.xml.rels" resolves to
// "ppt/media/image1.png".
func (p *PptxFile) BuildRelsIndex() error {
	p.relsIndex = map[string][]relRef{}

	for _, e := range p.Entries {
		if !strings.HasSuffix(e.name, ".rels") {
			continue
		}

		var rf relsFile
		if err := xml.Unmarshal(e.data, &rf); err != nil {
			return fmt.Errorf("parse rels %q: %w", e.name, err)
		}

		// baseDir is the folder that targets in this .rels resolve against.
		baseDir := relsBaseDir(e.name)

		for _, rel := range rf.Relationships {
			// External relationships (TargetMode="External") point outside the
			// package (e.g. hyperlinks) — they never name a media part.
			if strings.EqualFold(rel.Mode, "External") {
				continue
			}

			resolved := resolveTarget(baseDir, rel.Target)
			if !strings.HasPrefix(resolved, mediaPrefix) {
				continue // only index references to embedded media
			}

			p.relsIndex[resolved] = append(p.relsIndex[resolved], relRef{
				relsPart: e.name,
				id:       rel.Id,
				target:   rel.Target,
			})
		}
	}
	return nil
}

// RefCount returns how many relationships reference the given media part.
// Zero means the part is unused (an orphan) and may be removed.
func (p *PptxFile) RefCount(partName string) int {
	return len(p.relsIndex[partName])
}

// =============================================================================
// Placement — is the image ACTUALLY used, and where?
// =============================================================================
//
// A reference count on its own is misleading. A .rels file can contain a
// <Relationship> that targets a media part while NOTHING in the owning part's
// body actually references that relationship's Id. PowerPoint frequently leaves
// such "stale" relationships behind after a user deletes a picture, so the
// image is still stored in the file yet is invisible on every slide.
//
// To tell "really used" from "merely referenced" we confirm placement: a media
// relationship counts as USED only when its Id (e.g. "rId5") appears inside the
// XML body of the part that owns the .rels file (as an attribute value such as
// r:embed="rId5" or r:link="rId5"). Because a relationship Id is unique within
// its .rels file and always quoted where it is used, a simple quoted-substring
// scan of the owner's XML is a robust, format-agnostic test — we do not have to
// enumerate every element type that can carry an image.

// PlacementInfo summarises where a media part is genuinely placed within the
// package, as opposed to merely referenced by an unused relationship.
type PlacementInfo struct {
	// UsedOnSlide is true when the image is placed on at least one real slide
	// (an owning ppt/slides/slideN.xml references the relationship's Id).
	UsedOnSlide bool

	// Locations is the sorted, de-duplicated set of owner kinds where the image
	// is actually placed: any of "slide", "layout", "master", "notes",
	// "notes master" or "other". Empty means the image is placed nowhere.
	Locations []string
}

// MediaPlacement inspects every relationship that targets partName and reports
// where the image is genuinely placed (see the section comment above). It reads
// bytes only and never modifies the package. BuildRelsIndex must have run first.
func (p *PptxFile) MediaPlacement(partName string) PlacementInfo {
	var info PlacementInfo
	seen := map[string]bool{}

	for _, ref := range p.relsIndex[partName] {
		// The part whose .rels file this relationship lives in.
		owner := relsOwnerPart(ref.relsPart)
		oe := p.entry(owner)

		// Placement is confirmed only when the owning part's body actually
		// references the relationship Id. If the owner part or the Id is
		// missing we cannot confirm placement, so this reference does not count.
		placed := false
		if oe != nil && ref.id != "" {
			needle := []byte(`"` + ref.id + `"`)
			placed = bytes.Contains(oe.data, needle)
		}
		if !placed {
			continue
		}

		kind := ownerKind(owner)
		if kind == "slide" {
			info.UsedOnSlide = true
		}
		if !seen[kind] {
			seen[kind] = true
			info.Locations = append(info.Locations, kind)
		}
	}

	sort.Strings(info.Locations)
	return info
}

// relsOwnerPart returns the part that owns a given .rels file. A relationships
// file named "<dir>/_rels/<file>.rels" belongs to the part "<dir>/<file>".
// Example: "ppt/slides/_rels/slide1.xml.rels" -> "ppt/slides/slide1.xml".
// The package-level "_rels/.rels" has no owning part and returns "".
func relsOwnerPart(relsName string) string {
	parent := path.Dir(path.Dir(relsName)) // strip "_rels/<file>.rels" -> "<dir>"
	owner := strings.TrimSuffix(path.Base(relsName), ".rels")
	if parent == "." || parent == "" {
		return owner // e.g. package-level "_rels/.rels" -> ""
	}
	return parent + "/" + owner
}

// ownerKind classifies an owning part by its folder into a short usage label.
func ownerKind(owner string) string {
	switch {
	case strings.HasPrefix(owner, "ppt/slides/"):
		return "slide"
	case strings.HasPrefix(owner, "ppt/slideLayouts/"):
		return "layout"
	case strings.HasPrefix(owner, "ppt/slideMasters/"):
		return "master"
	case strings.HasPrefix(owner, "ppt/notesSlides/"):
		return "notes"
	case strings.HasPrefix(owner, "ppt/notesMasters/"):
		return "notes master"
	default:
		return "other"
	}
}

// relsBaseDir returns the folder that relationship targets in the given .rels
// file resolve against. "<dir>/_rels/<file>.rels" -> "<dir>".
// The package-level "_rels/.rels" resolves against the package root ("").
func relsBaseDir(relsName string) string {
	// Strip the trailing "_rels/<file>.rels" segment.
	dir := path.Dir(relsName) // ".../_rels"
	dir = path.Dir(dir)       // "..." (parent of _rels)
	if dir == "." {
		return ""
	}
	return dir
}

// resolveTarget joins a relationship target to its base folder and cleans the
// result into a package-absolute part name (no leading slash). Handles the
// "../" prefixes OOXML uses and absolute targets that begin with "/".
func resolveTarget(baseDir, target string) string {
	if strings.HasPrefix(target, "/") {
		// Absolute within the package.
		return strings.TrimPrefix(target, "/")
	}
	joined := path.Join(baseDir, target)
	return joined
}

// =============================================================================
// RenameMediaPart — atomic three-way rename for format conversion
// =============================================================================

// RenameMediaPart renames a media part from oldName to newName and keeps the
// package internally consistent, performing all three edits that a format
// change requires:
//
//  1. rename the ZIP entry,
//  2. rewrite the Target attribute in EVERY .rels file that referenced it,
//  3. ensure [Content_Types].xml declares a Default for the new extension.
//
// Only the file name/extension changes; the folder stays ppt/media/. Callers
// use this when converting e.g. image3.png → image3.jpeg. If any of the three
// edits were skipped PowerPoint would refuse to open the file, so they are done
// together here.
func (p *PptxFile) RenameMediaPart(oldName, newName string, contentType string) error {
	e := p.entry(oldName)
	if e == nil {
		return fmt.Errorf("rename: no such part %q", oldName)
	}
	if oldName == newName {
		return nil
	}
	if p.entry(newName) != nil {
		return fmt.Errorf("rename: target part %q already exists", newName)
	}

	// (1) Rename the entry in place (order preserved).
	e.name = newName

	// (2) Rewrite the target in every referencing .rels file. The target is a
	// relative path like "../media/image3.png"; only its final path segment (the
	// basename) changes, so we swap the old basename for the new one within the
	// exact Target="..." attribute to avoid touching anything else in the file.
	oldBase := path.Base(oldName)
	newBase := path.Base(newName)

	refs := p.relsIndex[oldName]
	for _, ref := range refs {
		re := p.entry(ref.relsPart)
		if re == nil {
			continue
		}
		// Compute the new raw target by replacing the basename suffix.
		newTarget := ref.target
		if strings.HasSuffix(ref.target, oldBase) {
			newTarget = ref.target[:len(ref.target)-len(oldBase)] + newBase
		} else {
			// Fallback: replace the last occurrence of the basename.
			if i := strings.LastIndex(ref.target, oldBase); i >= 0 {
				newTarget = ref.target[:i] + newBase + ref.target[i+len(oldBase):]
			}
		}

		// Replace the exact attribute occurrence: Target="<old>" -> Target="<new>".
		oldAttr := `Target="` + ref.target + `"`
		newAttr := `Target="` + newTarget + `"`
		re.data = bytes.Replace(re.data, []byte(oldAttr), []byte(newAttr), 1)
	}

	// Move the index entry to the new name, updating each ref's cached target.
	if refs != nil {
		updated := make([]relRef, len(refs))
		for i, ref := range refs {
			nt := ref.target
			if strings.HasSuffix(ref.target, oldBase) {
				nt = ref.target[:len(ref.target)-len(oldBase)] + newBase
			}
			ref.target = nt
			updated[i] = ref
		}
		p.relsIndex[newName] = updated
		delete(p.relsIndex, oldName)
	}

	// (3) Guarantee the new extension has a declared content type.
	ext := strings.TrimPrefix(path.Ext(newName), ".")
	if ext != "" {
		p.EnsureDefault(ext, contentType)
	}

	return nil
}

// ReplacePartData swaps the bytes of an existing part without renaming it.
// Used for same-format recompression (e.g. re-encoding a JPEG as a smaller
// JPEG) where the extension — and therefore rels and content types — is
// unchanged.
func (p *PptxFile) ReplacePartData(name string, data []byte) error {
	e := p.entry(name)
	if e == nil {
		return fmt.Errorf("replace: no such part %q", name)
	}
	e.data = data
	return nil
}

// =============================================================================
// RemoveMediaPart — drop an unreferenced part
// =============================================================================

// RemoveMediaPart deletes a media part from the package. It is only permitted
// when the part is unused (reference count zero); removing a referenced part
// would leave a dangling relationship and corrupt the file. Callers that need
// to neutralise a referenced part must replace its data instead (see the
// "remove" per-image override in the pipeline, which substitutes a 1×1 pixel).
func (p *PptxFile) RemoveMediaPart(name string) error {
	if p.RefCount(name) != 0 {
		return fmt.Errorf("refuse to remove referenced part %q (refCount=%d)", name, p.RefCount(name))
	}
	for i, e := range p.Entries {
		if e.name == name {
			p.Entries = append(p.Entries[:i], p.Entries[i+1:]...)
			delete(p.relsIndex, name)
			return nil
		}
	}
	return fmt.Errorf("remove: no such part %q", name)
}

// =============================================================================
// WritePptx — re-zip the package to disk
// =============================================================================

// bestCompressionRegistered guards a one-time registration below.
var bestCompressorName = "pptx-best-flate"

// WritePptx writes the in-memory package to outPath as a ZIP archive, entries
// in their original order. Deflated entries are re-compressed at
// flate.BestCompression; entries the source stored uncompressed (already-
// compressed media) stay stored so we do not waste time re-deflating them.
//
// It refuses to overwrite an existing file unless overwrite is true — the
// contract is to write a NEW <name>_compressed.pptx, never clobber the source.
func (p *PptxFile) WritePptx(outPath string, overwrite bool) (err error) {
	if !overwrite {
		if _, statErr := os.Stat(outPath); statErr == nil {
			return fmt.Errorf("refuse to overwrite existing file %q", outPath)
		}
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output %q: %w", outPath, err)
	}
	// On any failure after this point, remove the partial output so we never
	// leave a half-written .pptx behind.
	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(outPath)
		}
	}()

	zw := zip.NewWriter(f)

	// Register a Deflate compressor pinned to the best compression level. This
	// applies to every entry written with the Deflate method.
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})

	for _, e := range p.Entries {
		hdr := &zip.FileHeader{
			Name:   e.name,
			Method: e.method,
		}
		// Preserve the original method: Store for already-compressed media,
		// Deflate for XML. Deflate entries get the best-level compressor above.
		w, werr := zw.CreateHeader(hdr)
		if werr != nil {
			zw.Close()
			return fmt.Errorf("write entry %q: %w", e.name, werr)
		}
		if _, werr := w.Write(e.data); werr != nil {
			zw.Close()
			return fmt.Errorf("write entry %q: %w", e.name, werr)
		}
	}

	if cerr := zw.Close(); cerr != nil {
		return fmt.Errorf("finalize zip: %w", cerr)
	}
	return nil
}

// =============================================================================
// Round-trip safety — verify no relationship dangles
// =============================================================================

// VerifyRelationships re-checks that every media relationship in the package
// still resolves to an existing part. Called after a rewrite as a safety gate:
// a dangling target means the output would fail to open in PowerPoint, so the
// caller discards it. Returns an error naming the first broken reference.
func (p *PptxFile) VerifyRelationships() error {
	// Rebuild the index from the (possibly mutated) current entries.
	if err := p.BuildRelsIndex(); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, e := range p.Entries {
		names[e.name] = true
	}
	for part := range p.relsIndex {
		if !names[part] {
			return fmt.Errorf("dangling relationship: no part %q for referencing rels", part)
		}
	}
	return nil
}
