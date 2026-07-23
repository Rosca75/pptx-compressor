// =============================================================================
// compressor_test.go — Integration tests for the compression pipeline.
// =============================================================================

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// mediaSpec describes one media part to embed in a test deck.
type mediaSpec struct {
	name       string // e.g. "ppt/media/image1.png"
	data       []byte
	referenced bool // whether slide1 references it
}

// buildDeck writes a minimal but valid .pptx containing the given media parts.
// Referenced parts get a <Relationship> in slide1's rels.
func buildDeck(t *testing.T, media []mediaSpec) string {
	t.Helper()

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="png" ContentType="image/png"/>` +
		`<Default Extension="gif" ContentType="image/gif"/>` +
		`<Default Extension="emf" ContentType="image/x-emf"/>` +
		`<Default Extension="fntdata" ContentType="application/x-fontdata"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`</Types>`

	packageRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>` +
		`</Relationships>`

	presentation := `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:presentation>`
	presentationRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>` +
		`</Relationships>`
	slide := `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`

	// Build slide rels referencing the "referenced" media parts.
	slideRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	rid := 1
	for _, m := range media {
		if !m.referenced {
			continue
		}
		// Target is relative to ppt/slides.
		target := "../media/" + filepath.Base(m.name)
		slideRels += `<Relationship Id="rId` + itoa(rid) + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="` + target + `"/>`
		rid++
	}
	slideRels += `</Relationships>`

	dir := t.TempDir()
	outPath := filepath.Join(dir, "deck.pptx")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create deck: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	write := func(name string, data []byte, store bool) {
		method := zip.Deflate
		if store {
			method = zip.Store
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
		w.Write(data)
	}
	write("[Content_Types].xml", []byte(contentTypes), false)
	write("_rels/.rels", []byte(packageRels), false)
	write("ppt/presentation.xml", []byte(presentation), false)
	write("ppt/_rels/presentation.xml.rels", []byte(presentationRels), false)
	write("ppt/slides/slide1.xml", []byte(slide), false)
	write("ppt/slides/_rels/slide1.xml.rels", []byte(slideRels), false)
	for _, m := range media {
		write(m.name, m.data, true)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close deck zip: %v", err)
	}
	return outPath
}

// itoa is a tiny int→string helper to avoid importing strconv in the test file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// photoPNG builds a w×h opaque PNG with layered high-frequency detail —
// photographic-style content that a JPEG encoder compresses far smaller than a
// lossless PNG, so PNG→JPEG conversion is a genuine win (unlike a flat gradient,
// which PNG already stores compactly).
func photoPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := math.Sin(float64(x)/7) + math.Sin(float64(y)/9) +
				math.Sin(float64(x+y)/5) + math.Sin(float64(x-y)/11)
			b := uint8(128 + 60*v)
			img.Set(x, y, color.NRGBA{
				R: b,
				G: uint8(128 + 50*math.Sin(float64(x*y)/9000)),
				B: 255 - b,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode photo png: %v", err)
	}
	return buf.Bytes()
}

// runSync runs the pipeline synchronously and returns the final progress.
func runSync(t *testing.T, path string, opts CompressionOptions) ProgressResult {
	t.Helper()
	jobMutex.Lock()
	jobProgress = ProgressResult{}
	jobMutex.Unlock()

	runCompression(context.Background(), CompressionRequest{Path: path, Options: opts})

	jobMutex.Lock()
	defer jobMutex.Unlock()
	return jobProgress
}

func TestPipelineConvertsOpaquePngAndShrinks(t *testing.T) {
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.png", data: photoPNG(t, 900, 700), referenced: true},
	})

	prog := runSync(t, src, CompressionOptions{Preset: "balanced", MinSizeKB: 1})
	if prog.State != "done" {
		t.Fatalf("state = %q, errors=%v", prog.State, prog.Errors)
	}
	if prog.OutputPath == "" {
		t.Fatal("no output path")
	}
	if prog.FileBytesAfter >= prog.FileBytesBefore {
		t.Fatalf("output not smaller: before=%d after=%d", prog.FileBytesBefore, prog.FileBytesAfter)
	}

	// Re-open the output: image1.png should now be image1.jpeg, rels updated,
	// and every relationship must resolve.
	out, err := OpenPptx(prog.OutputPath)
	if err != nil {
		t.Fatalf("reopen output: %v", err)
	}
	if out.entry("ppt/media/image1.jpeg") == nil {
		t.Error("expected converted image1.jpeg in output")
	}
	if out.entry("ppt/media/image1.png") != nil {
		t.Error("old image1.png still present in output")
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("output relationships broken: %v", err)
	}
	rels := out.entry("ppt/slides/_rels/slide1.xml.rels")
	if !bytes.Contains(rels.data, []byte("image1.jpeg")) {
		t.Errorf("slide rels not updated: %s", rels.data)
	}
}

func TestPipelineReplaceOriginal(t *testing.T) {
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.png", data: photoPNG(t, 900, 700), referenced: true},
	})
	origSize := fileSize(src)

	// No "<name>_compressed.pptx" should exist yet.
	staging := compressedOutputPath(src)

	prog := runSync(t, src, CompressionOptions{Preset: "balanced", MinSizeKB: 1, ReplaceOriginal: true})
	if prog.State != "done" {
		t.Fatalf("state = %q, errors=%v", prog.State, prog.Errors)
	}

	// The output path must be the ORIGINAL file, not a _compressed copy.
	if prog.OutputPath != src {
		t.Errorf("output path = %q, want original %q", prog.OutputPath, src)
	}
	// The staging file must have been moved (renamed) onto the original, so it
	// no longer exists on its own.
	if _, err := os.Stat(staging); err == nil {
		t.Errorf("staging file %q should have been moved onto the original", staging)
	}
	// The original file must now be smaller than before.
	if got := fileSize(src); got >= origSize {
		t.Errorf("original not shrunk: before=%d after=%d", origSize, got)
	}
	// And it must still be a valid package with resolvable relationships.
	out, err := OpenPptx(src)
	if err != nil {
		t.Fatalf("reopen replaced original: %v", err)
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("replaced original relationships broken: %v", err)
	}
}

func TestPipelineNeverEnlarges(t *testing.T) {
	// A 1×1 PNG cannot be made smaller — it must be kept as-is.
	tiny := makePNG(t, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.png", data: tiny, referenced: true},
	})
	prog := runSync(t, src, CompressionOptions{Preset: "aggressive", MinSizeKB: 0})
	if prog.State != "done" {
		t.Fatalf("state = %q", prog.State)
	}
	if len(prog.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(prog.Results))
	}
	if prog.Results[0].Action != actKept && prog.Results[0].Action != actSkip {
		t.Errorf("tiny image action = %q, want kept/skip", prog.Results[0].Action)
	}
	// The part must be unchanged and still a png.
	out, _ := OpenPptx(prog.OutputPath)
	if out.entry("ppt/media/image1.png") == nil {
		t.Error("tiny png should have been kept unchanged")
	}
}

func TestPipelineRemovesUnusedMedia(t *testing.T) {
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.png", data: photoPNG(t, 400, 300), referenced: true},
		{name: "ppt/media/orphan.png", data: photoPNG(t, 200, 200), referenced: false},
	})
	prog := runSync(t, src, CompressionOptions{Preset: "balanced", MinSizeKB: 1, RemoveUnusedMedia: true})
	if prog.State != "done" {
		t.Fatalf("state = %q, errs=%v", prog.State, prog.Errors)
	}
	out, _ := OpenPptx(prog.OutputPath)
	if out.entry("ppt/media/orphan.png") != nil {
		t.Error("unused orphan.png should have been removed")
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("relationships broken after removal: %v", err)
	}
}

func TestPipelineCancellation(t *testing.T) {
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.png", data: photoPNG(t, 400, 300), referenced: true},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before starting work

	jobMutex.Lock()
	jobProgress = ProgressResult{}
	jobMutex.Unlock()

	runCompression(ctx, CompressionRequest{Path: src, Options: CompressionOptions{Preset: "balanced"}})

	jobMutex.Lock()
	state := jobProgress.State
	jobMutex.Unlock()
	if state != "cancelled" {
		t.Fatalf("state = %q, want cancelled", state)
	}
	// No output should have been produced.
	if _, err := os.Stat(compressedOutputPath(src)); err == nil {
		t.Error("cancelled run left an output file")
	}
}

// animatedGIFBytes builds a 2-frame animated GIF (must be passed through
// untouched by the pipeline).
func animatedGIFBytes(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White, color.RGBA{200, 50, 50, 255}}
	frame := func() *image.Paletted {
		return image.NewPaletted(image.Rect(0, 0, 16, 16), pal)
	}
	g := &gif.GIF{
		Image: []*image.Paletted{frame(), frame()},
		Delay: []int{10, 10},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode animated gif: %v", err)
	}
	return buf.Bytes()
}

// emfBytes builds a minimal EMF header (a vector part; must be skipped).
func emfBytes() []byte {
	b := make([]byte, 128)
	b[0] = 0x01 // EMR_HEADER record type
	copy(b[40:44], " EMF")
	return b
}

func TestPipelinePassesVectorsAndAnimatedGifUntouched(t *testing.T) {
	emf := emfBytes()
	anim := animatedGIFBytes(t)
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.emf", data: emf, referenced: true},
		{name: "ppt/media/image2.gif", data: anim, referenced: true},
		{name: "ppt/media/image3.png", data: photoPNG(t, 400, 300), referenced: true},
	})

	prog := runSync(t, src, CompressionOptions{Preset: "aggressive", MinSizeKB: 0})
	if prog.State != "done" {
		t.Fatalf("state = %q errs=%v", prog.State, prog.Errors)
	}

	out, err := OpenPptx(prog.OutputPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// EMF and animated GIF must survive byte-for-byte under their original names.
	if e := out.entry("ppt/media/image1.emf"); e == nil || !bytes.Equal(e.data, emf) {
		t.Error("EMF vector was modified or renamed")
	}
	if e := out.entry("ppt/media/image2.gif"); e == nil || !bytes.Equal(e.data, anim) {
		t.Error("animated GIF was modified or renamed")
	}
	// The photo PNG should have been converted (proving the run did real work).
	if out.entry("ppt/media/image3.jpeg") == nil {
		t.Error("photo png was not converted — pipeline did no work")
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("relationships broken: %v", err)
	}
}

// buildFontDeck writes a deck that embeds one font, references it from
// presentation.xml.rels, and lists it in an <p:embeddedFontLst> element.
func buildFontDeck(t *testing.T) string {
	t.Helper()
	contentTypes := `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="fntdata" ContentType="application/x-fontdata"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`</Types>`
	packageRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>` +
		`</Relationships>`
	presentation := `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:embeddedFontLst><p:embeddedFont><p:font typeface="Fancy"/><p:regular r:id="rId2"/></p:embeddedFont></p:embeddedFontLst>` +
		`</p:presentation>`
	presentationRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/font" Target="fonts/font1.fntdata"/>` +
		`</Relationships>`
	slide := `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`
	slideRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`

	dir := t.TempDir()
	outPath := filepath.Join(dir, "fonts.pptx")
	f, _ := os.Create(outPath)
	defer f.Close()
	zw := zip.NewWriter(f)
	write := func(name string, data []byte) {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		w.Write(data)
	}
	write("[Content_Types].xml", []byte(contentTypes))
	write("_rels/.rels", []byte(packageRels))
	write("ppt/presentation.xml", []byte(presentation))
	write("ppt/_rels/presentation.xml.rels", []byte(presentationRels))
	write("ppt/slides/slide1.xml", []byte(slide))
	write("ppt/slides/_rels/slide1.xml.rels", []byte(slideRels))
	write("ppt/fonts/font1.fntdata", make([]byte, 4096)) // dummy font payload
	zw.Close()
	return outPath
}

func TestStripEmbeddedFonts(t *testing.T) {
	src := buildFontDeck(t)
	prog := runSync(t, src, CompressionOptions{Preset: "balanced", StripEmbeddedFonts: true})
	if prog.State != "done" {
		t.Fatalf("state = %q errs=%v", prog.State, prog.Errors)
	}
	out, err := OpenPptx(prog.OutputPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// Font part removed.
	if out.entry("ppt/fonts/font1.fntdata") != nil {
		t.Error("font part not removed")
	}
	// embeddedFontLst stripped from presentation.xml.
	if pres := out.entry("ppt/presentation.xml"); bytes.Contains(pres.data, []byte("embeddedFontLst")) {
		t.Error("embeddedFontLst not stripped from presentation.xml")
	}
	// Font relationship removed from the rels.
	if rels := out.entry("ppt/_rels/presentation.xml.rels"); bytes.Contains(rels.data, []byte("font1.fntdata")) {
		t.Error("font relationship not removed from presentation rels")
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("relationships broken after font strip: %v", err)
	}
}

// buildTwoFontDeck writes a deck that embeds TWO font families: "Alpha" (two
// weights, ~8 KB total) and "Beta" (one weight, ~1 KB). Used to exercise the
// per-font inventory and selective strip.
func buildTwoFontDeck(t *testing.T) string {
	t.Helper()
	contentTypes := `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Default Extension="fntdata" ContentType="application/x-fontdata"/>` +
		`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
		`</Types>`
	packageRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>` +
		`</Relationships>`
	presentation := `<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:embeddedFontLst>` +
		`<p:embeddedFont><p:font typeface="Alpha"/><p:regular r:id="rId2"/><p:bold r:id="rId3"/></p:embeddedFont>` +
		`<p:embeddedFont><p:font typeface="Beta"/><p:regular r:id="rId4"/></p:embeddedFont>` +
		`</p:embeddedFontLst>` +
		`</p:presentation>`
	presentationRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/font" Target="fonts/font1.fntdata"/>` +
		`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/font" Target="fonts/font2.fntdata"/>` +
		`<Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/font" Target="fonts/font3.fntdata"/>` +
		`</Relationships>`
	slide := `<?xml version="1.0"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`
	slideRels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`

	dir := t.TempDir()
	outPath := filepath.Join(dir, "twofonts.pptx")
	f, _ := os.Create(outPath)
	defer f.Close()
	zw := zip.NewWriter(f)
	write := func(name string, data []byte) {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		w.Write(data)
	}
	write("[Content_Types].xml", []byte(contentTypes))
	write("_rels/.rels", []byte(packageRels))
	write("ppt/presentation.xml", []byte(presentation))
	write("ppt/_rels/presentation.xml.rels", []byte(presentationRels))
	write("ppt/slides/slide1.xml", []byte(slide))
	write("ppt/slides/_rels/slide1.xml.rels", []byte(slideRels))
	// Fonts are stored uncompressed, as PowerPoint writes them (and as real,
	// incompressible font data ends up), so the on-disk size equals the payload.
	writeStored := func(name string, data []byte) {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		w.Write(data)
	}
	writeStored("ppt/fonts/font1.fntdata", make([]byte, 5000)) // Alpha regular
	writeStored("ppt/fonts/font2.fntdata", make([]byte, 3000)) // Alpha bold
	writeStored("ppt/fonts/font3.fntdata", make([]byte, 1000)) // Beta regular
	zw.Close()
	return outPath
}

func TestEmbeddedFontsInventory(t *testing.T) {
	p, err := OpenPptx(buildTwoFontDeck(t))
	if err != nil {
		t.Fatal(err)
	}
	fonts := embeddedFonts(p)
	if len(fonts) != 2 {
		t.Fatalf("want 2 families, got %d", len(fonts))
	}
	// Largest first: Alpha (8000) before Beta (1000).
	if fonts[0].Typeface != "Alpha" || fonts[1].Typeface != "Beta" {
		t.Errorf("order = %q, %q; want Alpha, Beta", fonts[0].Typeface, fonts[1].Typeface)
	}
	if fonts[0].Bytes != 8000 || fonts[0].Weights != 2 {
		t.Errorf("Alpha bytes=%d weights=%d; want 8000/2", fonts[0].Bytes, fonts[0].Weights)
	}
	if fonts[1].Bytes != 1000 || fonts[1].Weights != 1 {
		t.Errorf("Beta bytes=%d weights=%d; want 1000/1", fonts[1].Bytes, fonts[1].Weights)
	}
	if fontPartsBytes(p) != 9000 {
		t.Errorf("fontPartsBytes = %d; want 9000", fontPartsBytes(p))
	}
}

func TestStripSelectedFontKeepsOthers(t *testing.T) {
	src := buildTwoFontDeck(t)
	prog := runSync(t, src, CompressionOptions{Preset: "balanced", RemoveFontTypefaces: []string{"Alpha"}})
	if prog.State != "done" {
		t.Fatalf("state = %q errs=%v", prog.State, prog.Errors)
	}
	out, err := OpenPptx(prog.OutputPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// Alpha's parts are gone; Beta's part remains.
	if out.entry("ppt/fonts/font1.fntdata") != nil || out.entry("ppt/fonts/font2.fntdata") != nil {
		t.Error("Alpha font parts not removed")
	}
	if out.entry("ppt/fonts/font3.fntdata") == nil {
		t.Error("Beta font part was removed but should be kept")
	}
	pres := out.entry("ppt/presentation.xml")
	if bytes.Contains(pres.data, []byte(`typeface="Alpha"`)) {
		t.Error("Alpha embeddedFont element not removed")
	}
	if !bytes.Contains(pres.data, []byte(`typeface="Beta"`)) {
		t.Error("Beta embeddedFont element wrongly removed")
	}
	// The list must survive because Beta is still embedded.
	if !bytes.Contains(pres.data, []byte("embeddedFontLst")) {
		t.Error("embeddedFontLst removed even though a family remains")
	}
	rels := out.entry("ppt/_rels/presentation.xml.rels")
	if bytes.Contains(rels.data, []byte("font1.fntdata")) || bytes.Contains(rels.data, []byte("font2.fntdata")) {
		t.Error("Alpha font relationships not removed")
	}
	if !bytes.Contains(rels.data, []byte("font3.fntdata")) {
		t.Error("Beta font relationship wrongly removed")
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("relationships broken after selective font strip: %v", err)
	}
	// The remaining inventory should be just Beta.
	if err := out.BuildRelsIndex(); err != nil {
		t.Fatal(err)
	}
	rem := embeddedFonts(out)
	if len(rem) != 1 || rem[0].Typeface != "Beta" {
		t.Errorf("remaining fonts = %+v; want [Beta]", rem)
	}
}

func TestStripAllSelectedFontsDropsList(t *testing.T) {
	src := buildTwoFontDeck(t)
	prog := runSync(t, src, CompressionOptions{Preset: "balanced", RemoveFontTypefaces: []string{"Alpha", "Beta"}})
	if prog.State != "done" {
		t.Fatalf("state = %q errs=%v", prog.State, prog.Errors)
	}
	out, err := OpenPptx(prog.OutputPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	pres := out.entry("ppt/presentation.xml")
	if bytes.Contains(pres.data, []byte("embeddedFontLst")) {
		t.Error("embeddedFontLst should be dropped when no family remains")
	}
	for _, part := range []string{"font1", "font2", "font3"} {
		if out.entry("ppt/fonts/"+part+".fntdata") != nil {
			t.Errorf("%s not removed", part)
		}
	}
	if err := out.VerifyRelationships(); err != nil {
		t.Errorf("relationships broken: %v", err)
	}
}

func TestAnalyzeComposition(t *testing.T) {
	app := &App{}
	res := app.AnalyzePptx(buildTwoFontDeck(t))
	if res.Error != "" {
		t.Fatalf("analyze error: %s", res.Error)
	}
	if len(res.Fonts) != 2 {
		t.Errorf("Fonts len = %d; want 2", len(res.Fonts))
	}
	if res.FontBytes != 9000 {
		t.Errorf("FontBytes = %d; want 9000", res.FontBytes)
	}
	if !res.HasEmbeddedFonts {
		t.Error("HasEmbeddedFonts should be true")
	}
	// The four composition buckets must never exceed the whole file.
	sum := res.ImageBytes + res.VideoBytes + res.FontBytes + res.OtherBytes
	if sum > res.FileBytes {
		t.Errorf("composition sum %d exceeds fileBytes %d", sum, res.FileBytes)
	}
	if res.FontBytes > res.FileBytes {
		t.Errorf("FontBytes %d exceeds fileBytes %d", res.FontBytes, res.FileBytes)
	}
}

func TestAlreadyOptimizedDeckKeepsOriginals(t *testing.T) {
	// A deck whose only image is a small, already-compressed JPEG should mostly
	// report "kept" (no gain) rather than enlarge anything.
	photo := photoPNG(t, 300, 200)
	jpg, err := encodeJPEG(mustDecode(t, photo), 60) // pre-compress hard
	if err != nil {
		t.Fatalf("pre-encode: %v", err)
	}
	src := buildDeck(t, []mediaSpec{
		{name: "ppt/media/image1.jpeg", data: jpg, referenced: true},
	})
	prog := runSync(t, src, CompressionOptions{Preset: "light", MinSizeKB: 0})
	if prog.State != "done" {
		t.Fatalf("state = %q", prog.State)
	}
	// Never-larger guarantee: output must not exceed input.
	if prog.FileBytesAfter > prog.FileBytesBefore {
		t.Errorf("already-optimized deck grew: %d -> %d", prog.FileBytesBefore, prog.FileBytesAfter)
	}
}

// mustDecode decodes image bytes or fails the test.
func mustDecode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := decodeImage(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}
