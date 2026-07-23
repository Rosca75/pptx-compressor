// =============================================================================
// display_test.go — Tests for on-slide display-size extraction and the
// "resize to on-slide size" cap.
// =============================================================================
//
// These verify the new pipeline for smart-resize-by-usage:
//   - buildDisplaySizes parses <p:pic>/<p:sp> extents from slide/layout/master
//     XML, resolves each to its media part, inflates for crops, takes the max
//     across placements, and skips grouped shapes.
//   - emuToPx / effectiveMaxEdge convert that into the per-image downscale cap.
//   - DecideAction and transformPart honour the cap end to end.
// =============================================================================

package main

import (
	"context"
	"testing"
)

// mkEntry builds an in-memory zip entry from a name and string body.
func mkEntry(name, body string) *zipEntry { return &zipEntry{name: name, data: []byte(body)} }

// imageRels builds a minimal .rels document with one image relationship.
func imageRels(id, target string) string {
	return `<?xml version="1.0"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="` + id + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="` + target + `"/>` +
		`</Relationships>`
}

// pic builds a <p:pic> with an embed id and an EMU display box.
func pic(embed string, cx, cy int64) string {
	return `<p:pic><p:blipFill><a:blip r:embed="` + embed + `"/></p:blipFill>` +
		`<p:spPr><a:xfrm><a:ext cx="` + itoa64(cx) + `" cy="` + itoa64(cy) + `"/></a:xfrm></p:spPr></p:pic>`
}

// picCropped is like pic but with an <a:srcRect> crop (values in 1000ths %).
func picCropped(embed string, cx, cy int64, l, r, tt, b int) string {
	return `<p:pic><p:blipFill><a:blip r:embed="` + embed + `"/>` +
		`<a:srcRect l="` + itoa(l) + `" r="` + itoa(r) + `" t="` + itoa(tt) + `" b="` + itoa(b) + `"/></p:blipFill>` +
		`<p:spPr><a:xfrm><a:ext cx="` + itoa64(cx) + `" cy="` + itoa64(cy) + `"/></a:xfrm></p:spPr></p:pic>`
}

// spFill builds a <p:sp> whose fill is a picture (blipFill under spPr).
func spFill(embed string, cx, cy int64) string {
	return `<p:sp><p:spPr><a:xfrm><a:ext cx="` + itoa64(cx) + `" cy="` + itoa64(cy) + `"/></a:xfrm>` +
		`<a:blipFill><a:blip r:embed="` + embed + `"/></a:blipFill></p:spPr></p:sp>`
}

// slideDoc wraps shape-tree bodies in a slide root.
func slideDoc(body string) string {
	return `<p:sld><p:cSld><p:spTree>` + body + `</p:spTree></p:cSld></p:sld>`
}

// itoa64 is a tiny int64→string helper (avoids importing strconv here).
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// TestBuildDisplaySizes covers the parser: basic extraction, max across
// placements, crop inflation, shape-fill pictures, and the group-skip rule.
func TestBuildDisplaySizes(t *testing.T) {
	const inch = int64(914400) // 1 inch in EMU

	p := &PptxFile{
		ctDefaults:  map[string]string{},
		ctOverrides: map[string]string{},
		relsIndex:   map[string][]relRef{},
		Entries: []*zipEntry{
			// basic.png: shown at 1×0.5 inch → longest edge 1 inch.
			mkEntry("ppt/slides/slide1.xml", slideDoc(pic("rId1", inch, inch/2))),
			mkEntry("ppt/slides/_rels/slide1.xml.rels", imageRels("rId1", "../media/basic.png")),

			// bigger.png: appears on two slides — 1 inch on slide2, 3 inch on
			// slide3. The MAX (3 inch) must win.
			mkEntry("ppt/slides/slide2.xml", slideDoc(pic("rId1", inch, inch))),
			mkEntry("ppt/slides/_rels/slide2.xml.rels", imageRels("rId1", "../media/bigger.png")),
			mkEntry("ppt/slides/slide3.xml", slideDoc(pic("rId1", 3*inch, inch))),
			mkEntry("ppt/slides/_rels/slide3.xml.rels", imageRels("rId1", "../media/bigger.png")),

			// cropped.png: box is 1 inch wide but only the left 50% is shown
			// (r=50000), so the full image needs 2 inch of width.
			mkEntry("ppt/slides/slide4.xml", slideDoc(picCropped("rId1", inch, inch, 0, 50000, 0, 0))),
			mkEntry("ppt/slides/_rels/slide4.xml.rels", imageRels("rId1", "../media/cropped.png")),

			// fill.png: used as a shape's picture fill at 2×1 inch.
			mkEntry("ppt/slides/slide5.xml", slideDoc(spFill("rId1", 2*inch, inch))),
			mkEntry("ppt/slides/_rels/slide5.xml.rels", imageRels("rId1", "../media/fill.png")),

			// grouped.png: inside a <p:grpSp> group → intentionally NOT measured.
			mkEntry("ppt/slides/slide6.xml", slideDoc(`<p:grpSp>`+pic("rId1", 5*inch, 5*inch)+`</p:grpSp>`)),
			mkEntry("ppt/slides/_rels/slide6.xml.rels", imageRels("rId1", "../media/grouped.png")),

			// The media parts.
			mkEntry("ppt/media/basic.png", "x"),
			mkEntry("ppt/media/bigger.png", "x"),
			mkEntry("ppt/media/cropped.png", "x"),
			mkEntry("ppt/media/fill.png", "x"),
			mkEntry("ppt/media/grouped.png", "x"),
		},
	}
	if err := p.BuildRelsIndex(); err != nil {
		t.Fatalf("build rels index: %v", err)
	}

	cases := []struct {
		part    string
		wantEmu int64
	}{
		{"ppt/media/basic.png", inch},
		{"ppt/media/bigger.png", 3 * inch},
		{"ppt/media/cropped.png", 2 * inch}, // 1 inch / 0.5 visible
		{"ppt/media/fill.png", 2 * inch},
		{"ppt/media/grouped.png", 0}, // grouped → not measured
	}
	for _, c := range cases {
		if got := p.DisplayEdgeEmu(c.part); got != c.wantEmu {
			t.Errorf("DisplayEdgeEmu(%s) = %d, want %d", c.part, got, c.wantEmu)
		}
	}
}

// TestEmuToPx checks the EMU→pixel conversion at a few DPIs.
func TestEmuToPx(t *testing.T) {
	const inch = int64(914400)
	cases := []struct {
		emu  int64
		dpi  int
		want int
	}{
		{inch, 96, 96},
		{inch, 150, 150},
		{2 * inch, 220, 440},
		{inch, 300, 300},
		{0, 150, 0},  // no size
		{inch, 0, 0}, // no dpi
		{-5, 150, 0}, // negative guarded
	}
	for _, c := range cases {
		if got := emuToPx(c.emu, c.dpi); got != c.want {
			t.Errorf("emuToPx(%d, %d) = %d, want %d", c.emu, c.dpi, got, c.want)
		}
	}
}

// TestEffectiveMaxEdge checks how the global cap and the display cap combine.
func TestEffectiveMaxEdge(t *testing.T) {
	const inch = int64(914400)
	// An image displayed at 2 inches → 300px at 150 DPI.
	m := MediaInfo{DisplayMaxEdgeEmu: 2 * inch}

	// Feature off: only the global cap applies.
	if got := effectiveMaxEdge(m, CompressionOptions{MaxEdgePx: 1920, DisplayTargetDpi: 150}); got != 1920 {
		t.Errorf("feature off: got %d, want 1920", got)
	}
	// Feature on, display cap (300) tighter than global (1920) → 300 wins.
	on := CompressionOptions{MaxEdgePx: 1920, ResizeToDisplaySize: true, DisplayTargetDpi: 150}
	if got := effectiveMaxEdge(m, on); got != 300 {
		t.Errorf("display tighter: got %d, want 300", got)
	}
	// Feature on but global cap tighter (200 < 300) → global wins.
	on2 := CompressionOptions{MaxEdgePx: 200, ResizeToDisplaySize: true, DisplayTargetDpi: 150}
	if got := effectiveMaxEdge(m, on2); got != 200 {
		t.Errorf("global tighter: got %d, want 200", got)
	}
	// Feature on, no global cap → display cap applies alone.
	on3 := CompressionOptions{MaxEdgePx: 0, ResizeToDisplaySize: true, DisplayTargetDpi: 150}
	if got := effectiveMaxEdge(m, on3); got != 300 {
		t.Errorf("no global cap: got %d, want 300", got)
	}
	// Feature on but display size unknown → falls back to the global cap.
	unknown := MediaInfo{DisplayMaxEdgeEmu: 0}
	if got := effectiveMaxEdge(unknown, on); got != 1920 {
		t.Errorf("unknown display: got %d, want 1920", got)
	}
	// Higher DPI keeps more pixels: 2 inch at 300 DPI = 600px.
	onHi := CompressionOptions{MaxEdgePx: 0, ResizeToDisplaySize: true, DisplayTargetDpi: 300}
	if got := effectiveMaxEdge(m, onHi); got != 600 {
		t.Errorf("300 dpi: got %d, want 600", got)
	}
}

// TestDecideActionUsesDisplayCap verifies the decision matrix stamps the
// display-derived cap onto Action.MaxEdge when the feature is on.
func TestDecideActionUsesDisplayCap(t *testing.T) {
	const inch = int64(914400)
	m := MediaInfo{
		PartName:          "ppt/media/photo.jpg",
		Format:            fmtJPEG,
		Bytes:             500 * 1024,
		DisplayMaxEdgeEmu: 2 * inch, // 300px at 150 DPI
	}
	// Feature on with the Light preset (global cap 2560): the display cap (300)
	// is tighter, so it wins.
	act := DecideAction(m, CompressionOptions{Preset: "light", ResizeToDisplaySize: true, DisplayTargetDpi: 150})
	if act.MaxEdge != 300 {
		t.Errorf("Action.MaxEdge = %d, want 300 (display cap)", act.MaxEdge)
	}
	// Feature off with the Light preset: the display cap is ignored and only the
	// preset's global cap (2560) applies.
	act = DecideAction(m, CompressionOptions{Preset: "light"})
	if act.MaxEdge != 2560 {
		t.Errorf("feature off: Action.MaxEdge = %d, want 2560 (global cap only)", act.MaxEdge)
	}
}

// TestTransformPartDownscalesToDisplay is an end-to-end check: a physically
// large photo shown small on the slide is actually decoded, downscaled and
// re-encoded to (about) the display cap — and the result is smaller.
func TestTransformPartDownscalesToDisplay(t *testing.T) {
	const inch = int64(914400)
	// A 2000×2000 photographic PNG shown in a 1-inch box.
	data := photoPNG(t, 2000, 2000)
	e := &zipEntry{name: "ppt/media/big.png", data: data}
	m := MediaInfo{
		PartName:          "ppt/media/big.png",
		Format:            fmtPNG,
		Bytes:             int64(len(data)),
		Width:             2000,
		Height:            2000,
		DisplayMaxEdgeEmu: inch, // 150px at 150 DPI
	}
	opts := CompressionOptions{
		ResizeToDisplaySize: true,
		DisplayTargetDpi:    150,
		ConvertOpaquePng:    true, // opaque PNG → JPEG path
		MinSizeKB:           1,
	}

	res := transformPart(context.Background(), e, m, resolveOptions(opts))
	if res.err != nil {
		t.Fatalf("transformPart error: %v", res.err)
	}
	if res.newData == nil {
		t.Fatalf("expected re-encoded bytes, got none (action %q)", res.action)
	}
	if int64(len(res.newData)) >= m.Bytes {
		t.Errorf("output not smaller: %d >= %d", len(res.newData), m.Bytes)
	}
	// Decode the output and confirm its longest edge was capped near 150px.
	img, _, err := decodeImage(res.newData)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	b := img.Bounds()
	longest := b.Dx()
	if b.Dy() > longest {
		longest = b.Dy()
	}
	if longest > 160 { // 150px + small rounding headroom
		t.Errorf("output longest edge = %d px, want ~150", longest)
	}
}

// TestTransformPartNoResizeWhenFeatureOff confirms the feature is inert when
// off: the same large-photo-in-small-box image is NOT downscaled to display
// size (it keeps its full 2000px resolution, subject only to encoding).
func TestTransformPartNoResizeWhenFeatureOff(t *testing.T) {
	const inch = int64(914400)
	data := photoPNG(t, 2000, 2000)
	e := &zipEntry{name: "ppt/media/big.png", data: data}
	m := MediaInfo{
		PartName:          "ppt/media/big.png",
		Format:            fmtPNG,
		Bytes:             int64(len(data)),
		Width:             2000,
		Height:            2000,
		DisplayMaxEdgeEmu: inch,
	}
	// Feature OFF. Use the Light preset (global cap 2560) so the 2000px image is
	// larger than nothing yet within the cap → it must NOT be downscaled at all.
	opts := resolveOptions(CompressionOptions{Preset: "light", ConvertOpaquePng: true, MinSizeKB: 1})

	res := transformPart(context.Background(), e, m, opts)
	if res.err != nil {
		t.Fatalf("transformPart error: %v", res.err)
	}
	if res.newData == nil {
		t.Skip("re-encode kept original; nothing to check for dimensions")
	}
	img, _, err := decodeImage(res.newData)
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if img.Bounds().Dx() != 2000 || img.Bounds().Dy() != 2000 {
		t.Errorf("dimensions changed with feature off: %dx%d, want 2000x2000",
			img.Bounds().Dx(), img.Bounds().Dy())
	}
}
