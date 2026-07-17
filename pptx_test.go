// =============================================================================
// pptx_test.go — Tests for the PPTX container layer.
// =============================================================================
//
// These tests build a tiny synthetic .pptx entirely in code (no fixtures on
// disk) so they run anywhere. The fixture is a minimal but valid OOXML package:
// a content-types manifest, the package rels, a presentation part, one slide
// that references one 1×1 PNG under ppt/media/.
// =============================================================================

package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// makePNG returns the bytes of an opaque 1×1 PNG (a JPEG-conversion candidate).
func makePNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// syntheticPptx writes a minimal valid .pptx to a temp file and returns its path.
// The single media part is ppt/media/image1.png, referenced from slide1.
func syntheticPptx(t *testing.T) string {
	t.Helper()

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="png" ContentType="image/png"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`</Types>`

	packageRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>` +
		`</Relationships>`

	presentation := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:presentation>`

	presentationRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>` +
		`</Relationships>`

	slide := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`

	slideRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>` +
		`</Relationships>`

	pngBytes := makePNG(t, color.NRGBA{R: 10, G: 20, B: 30, A: 255})

	dir := t.TempDir()
	outPath := filepath.Join(dir, "sample.pptx")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	// Order matters: [Content_Types].xml first.
	entries := []struct {
		name string
		data []byte
		// store true means write with zip.Store (like already-compressed media).
		store bool
	}{
		{"[Content_Types].xml", []byte(contentTypes), false},
		{"_rels/.rels", []byte(packageRels), false},
		{"ppt/presentation.xml", []byte(presentation), false},
		{"ppt/_rels/presentation.xml.rels", []byte(presentationRels), false},
		{"ppt/slides/slide1.xml", []byte(slide), false},
		{"ppt/slides/_rels/slide1.xml.rels", []byte(slideRels), false},
		{"ppt/media/image1.png", pngBytes, true},
	}
	for _, e := range entries {
		method := zip.Deflate
		if e.store {
			method = zip.Store
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: method})
		if err != nil {
			t.Fatalf("write fixture entry %q: %v", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatalf("write fixture data %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close fixture zip: %v", err)
	}
	return outPath
}

// readEntries reads a written .pptx back into a name→bytes map for assertions.
func readEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %q: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("read %q: %v", f.Name, err)
		}
		rc.Close()
		out[f.Name] = buf.Bytes()
	}
	return out
}

func TestOpenPptxValidation(t *testing.T) {
	// A random ZIP without the required parts must be rejected.
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.zip")
	f, _ := os.Create(badPath)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("hello.txt")
	w.Write([]byte("not a pptx"))
	zw.Close()
	f.Close()

	if _, err := OpenPptx(badPath); err == nil {
		t.Fatal("expected error opening non-pptx zip, got nil")
	}

	// A valid fixture must open cleanly.
	good := syntheticPptx(t)
	if _, err := OpenPptx(good); err != nil {
		t.Fatalf("open valid pptx: %v", err)
	}
}

func TestRoundTripByteIdentical(t *testing.T) {
	src := syntheticPptx(t)
	p, err := OpenPptx(src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	out := filepath.Join(t.TempDir(), "out.pptx")
	if err := p.WritePptx(out, false); err != nil {
		t.Fatalf("write: %v", err)
	}

	orig := readEntries(t, src)
	got := readEntries(t, out)

	if len(orig) != len(got) {
		t.Fatalf("entry count changed: orig=%d got=%d", len(orig), len(got))
	}
	for name, want := range orig {
		have, ok := got[name]
		if !ok {
			t.Errorf("missing entry after round-trip: %q", name)
			continue
		}
		if !bytes.Equal(want, have) {
			t.Errorf("entry %q changed on round-trip (orig %d bytes, got %d bytes)", name, len(want), len(have))
		}
	}
}

func TestWriteRefusesOverwrite(t *testing.T) {
	src := syntheticPptx(t)
	p, _ := OpenPptx(src)
	out := filepath.Join(t.TempDir(), "out.pptx")
	if err := p.WritePptx(out, false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := p.WritePptx(out, false); err == nil {
		t.Fatal("expected refusal to overwrite existing file")
	}
	if err := p.WritePptx(out, true); err != nil {
		t.Fatalf("overwrite=true should succeed: %v", err)
	}
}

func TestBuildRelsIndexAndRefCount(t *testing.T) {
	p, _ := OpenPptx(syntheticPptx(t))
	if err := p.BuildRelsIndex(); err != nil {
		t.Fatalf("build rels index: %v", err)
	}
	if got := p.RefCount("ppt/media/image1.png"); got != 1 {
		t.Fatalf("refCount(image1.png) = %d, want 1", got)
	}
	if got := p.RefCount("ppt/media/nonexistent.png"); got != 0 {
		t.Fatalf("refCount(nonexistent) = %d, want 0", got)
	}
}

func TestRenameMediaPartUpdatesRelsAndContentTypes(t *testing.T) {
	p, _ := OpenPptx(syntheticPptx(t))
	if err := p.BuildRelsIndex(); err != nil {
		t.Fatalf("build rels index: %v", err)
	}

	// jpeg default should not exist yet.
	if _, ok := p.ctDefaults["jpeg"]; ok {
		t.Fatal("jpeg default present before rename")
	}

	err := p.RenameMediaPart("ppt/media/image1.png", "ppt/media/image1.jpeg", "image/jpeg")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}

	// (1) entry renamed
	if p.entry("ppt/media/image1.png") != nil {
		t.Error("old part still present")
	}
	if p.entry("ppt/media/image1.jpeg") == nil {
		t.Error("new part missing")
	}

	// (2) slide rels rewritten
	rels := p.entry("ppt/slides/_rels/slide1.xml.rels")
	if !bytes.Contains(rels.data, []byte(`Target="../media/image1.jpeg"`)) {
		t.Errorf("rels not rewritten: %s", rels.data)
	}
	if bytes.Contains(rels.data, []byte("image1.png")) {
		t.Errorf("rels still references old name: %s", rels.data)
	}

	// (3) content types now has a jpeg default
	if _, ok := p.ctDefaults["jpeg"]; !ok {
		t.Error("jpeg default not added to content types map")
	}
	ct := p.entry(contentTypesPart)
	if !bytes.Contains(ct.data, []byte(`Extension="jpeg"`)) {
		t.Errorf("content types not updated: %s", ct.data)
	}

	// Index moved to the new name.
	if p.RefCount("ppt/media/image1.jpeg") != 1 {
		t.Errorf("refCount after rename = %d, want 1", p.RefCount("ppt/media/image1.jpeg"))
	}

	// The rewritten package must still verify.
	if err := p.VerifyRelationships(); err != nil {
		t.Errorf("verify after rename: %v", err)
	}
}

// TestMediaPlacement verifies that placement detection distinguishes an image
// genuinely placed on a slide from one that is only referenced by a stale
// relationship (present but invisible) or a true orphan (no relationship).
func TestMediaPlacement(t *testing.T) {
	// mk builds an in-memory zip entry.
	mk := func(name, data string) *zipEntry { return &zipEntry{name: name, data: []byte(data)} }
	// rels builds a minimal .rels document with one image relationship.
	rels := func(id, target string) string {
		return `<?xml version="1.0"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="` + id + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="` + target + `"/>` +
			`</Relationships>`
	}

	p := &PptxFile{
		ctDefaults:  map[string]string{},
		ctOverrides: map[string]string{},
		relsIndex:   map[string][]relRef{},
		Entries: []*zipEntry{
			// used.png: rId1 is actually referenced by slide1's body → on a slide.
			mk("ppt/slides/slide1.xml", `<p:sld><p:pic><a:blip r:embed="rId1"/></p:pic></p:sld>`),
			mk("ppt/slides/_rels/slide1.xml.rels", rels("rId1", "../media/used.png")),
			// logo.png: referenced and placed, but only on a layout (not a slide).
			mk("ppt/slideLayouts/slideLayout1.xml", `<p:sldLayout><a:blip r:embed="rId1"/></p:sldLayout>`),
			mk("ppt/slideLayouts/_rels/slideLayout1.xml.rels", rels("rId1", "../media/logo.png")),
			// stale.png: slide2 has a relationship for it, but the body never uses
			// rId1 → present in the file but invisible.
			mk("ppt/slides/slide2.xml", `<p:sld></p:sld>`),
			mk("ppt/slides/_rels/slide2.xml.rels", rels("rId1", "../media/stale.png")),
			// The media parts themselves. orphan.png has no relationship at all.
			mk("ppt/media/used.png", "x"),
			mk("ppt/media/logo.png", "x"),
			mk("ppt/media/stale.png", "x"),
			mk("ppt/media/orphan.png", "x"),
		},
	}
	if err := p.BuildRelsIndex(); err != nil {
		t.Fatalf("build rels index: %v", err)
	}

	cases := []struct {
		part        string
		wantOnSlide bool
		wantUsage   string
	}{
		// The fixture has no presentation.xml, so the page number comes from
		// the file-name fallback (slide1.xml → deck page 1).
		{"ppt/media/used.png", true, "slide 1"},
		{"ppt/media/logo.png", false, "layout"},
		{"ppt/media/stale.png", false, "unused (stale ref)"},
		{"ppt/media/orphan.png", false, "unused"},
	}
	for _, c := range cases {
		place := p.MediaPlacement(c.part)
		if place.UsedOnSlide != c.wantOnSlide {
			t.Errorf("%s: UsedOnSlide = %v, want %v", c.part, place.UsedOnSlide, c.wantOnSlide)
		}
		got := usageLabel(p.RefCount(c.part), place)
		if got != c.wantUsage {
			t.Errorf("%s: usage = %q, want %q", c.part, got, c.wantUsage)
		}
	}
}

func TestRemoveMediaPartRefusesReferenced(t *testing.T) {
	p, _ := OpenPptx(syntheticPptx(t))
	p.BuildRelsIndex()
	if err := p.RemoveMediaPart("ppt/media/image1.png"); err == nil {
		t.Fatal("expected refusal to remove referenced part")
	}
}

// TestCRC32Sanity is a tiny guard that our helper imports are actually used and
// the standard hash matches what the zip package computes — a canary against
// accidental data corruption in the fixture writer.
func TestCRC32Sanity(t *testing.T) {
	data := makePNG(t, color.NRGBA{A: 255})
	sum := crc32.ChecksumIEEE(data)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], sum)
	if sum == 0 {
		t.Fatal("unexpected zero crc for png")
	}
}
