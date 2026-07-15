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
