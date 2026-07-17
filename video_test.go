// =============================================================================
// video_test.go — Tests for video detection, the placeholder, the video
//                 decision paths, and slide-page numbering.
// =============================================================================

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mp4Header returns bytes that sniff as an ISO-BMFF file with the given major
// brand ("isom" for video, "M4A " for audio-only).
func mp4Header(brand string) []byte {
	return append(append([]byte{0, 0, 0, 0x20}, []byte("ftyp")...), []byte(brand+"AAAAAAAAAAAAAAAA")...)
}

func TestVideoDetection(t *testing.T) {
	// MP4 video is detected by bytes.
	if !isVideoPart("ppt/media/media1.mp4", mp4Header("isom")) {
		t.Error("isom ftyp should be a video")
	}
	if !isMP4Data(mp4Header("isom")) {
		t.Error("isom ftyp should be MP4")
	}
	// Audio-only M4A must NOT count as a video (extension is not a video one).
	if isVideoPart("ppt/media/media1.m4a", mp4Header("M4A ")) {
		t.Error("M4A audio must not be classified as video")
	}
	// Unknown bytes fall back to a video extension.
	if !isVideoPart("ppt/media/media2.wmv", []byte("garbage-bytes-here")) {
		t.Error("wmv extension fallback should classify as video")
	}
	// An image is never a video.
	if isVideoPart("ppt/media/image1.png", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}) {
		t.Error("png must not be classified as video")
	}
}

func TestPlaceholderMP4IsValidAndSmall(t *testing.T) {
	ph := placeholderMP4()
	if len(ph) == 0 || len(ph) > 8*1024 {
		t.Fatalf("placeholder size = %d, want small and non-empty", len(ph))
	}
	if !isMP4Data(ph) {
		t.Fatal("placeholder must itself sniff as an MP4 video")
	}
	if detectFormat(ph) != fmtMedia {
		t.Fatalf("placeholder detectFormat = %q, want %q", detectFormat(ph), fmtMedia)
	}
}

func TestDecideVideoRemoveWinsOverCompress(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/media1.mp4", Format: fmtMedia, Bytes: 5 << 20, IsVideo: true, IsMp4: true}

	// Default: videos are skipped.
	if a := DecideAction(m, CompressionOptions{Preset: "balanced"}); a.Kind != actSkip {
		t.Fatalf("default video → got %+v, want skip", a)
	}
	// Compression level selected → compress-video with that level.
	a := DecideAction(m, CompressionOptions{Preset: "balanced", VideoCompression: "aggressive"})
	if a.Kind != actCompressVideo || a.VideoLevel != "aggressive" {
		t.Fatalf("video compression → got %+v, want compress-video/aggressive", a)
	}
	// RemoveVideos wins over a selected compression level.
	a = DecideAction(m, CompressionOptions{Preset: "balanced", RemoveVideos: true, VideoCompression: "light"})
	if a.Kind != actRemoveVideo {
		t.Fatalf("remove videos → got %+v, want remove-video", a)
	}
	// Non-MP4 videos (e.g. WMV) cannot be compressed — but can be removed.
	wmv := MediaInfo{PartName: "ppt/media/media2.wmv", Format: fmtUnknown, Bytes: 5 << 20, IsVideo: true, IsMp4: false}
	if a := DecideAction(wmv, CompressionOptions{Preset: "balanced", VideoCompression: "light"}); a.Kind != actSkip {
		t.Fatalf("wmv compression → got %+v, want skip", a)
	}
	if a := DecideAction(wmv, CompressionOptions{Preset: "balanced", RemoveVideos: true}); a.Kind != actRemoveVideo {
		t.Fatalf("wmv removal → got %+v, want remove-video", a)
	}
}

func TestTransformPartRemoveVideoUsesPlaceholder(t *testing.T) {
	// A 1 MB fake MP4 part.
	data := append(mp4Header("isom"), make([]byte, 1<<20)...)
	e := &zipEntry{name: "ppt/media/media1.mp4", data: data}
	m := MediaInfo{PartName: e.name, Format: fmtMedia, Bytes: int64(len(data)), IsVideo: true, IsMp4: true}

	r := transformPart(context.Background(), e, m, CompressionOptions{Preset: "balanced", RemoveVideos: true})
	if r.action != actRemoveVideo {
		t.Fatalf("action = %q, want %q", r.action, actRemoveVideo)
	}
	if len(r.newData) == 0 || len(r.newData) >= len(data) {
		t.Fatalf("placeholder proposal size = %d, want smaller than %d", len(r.newData), len(data))
	}
	// The name must not change — relationships and content type stay valid.
	if r.newName != r.oldName {
		t.Fatalf("remove-video must not rename the part (%q → %q)", r.oldName, r.newName)
	}
}

func TestEstimateVideoActions(t *testing.T) {
	m := MediaInfo{PartName: "ppt/media/media1.mp4", Format: fmtMedia, Bytes: 10 << 20, IsVideo: true, IsMp4: true}

	// RemoveVideos estimates the placeholder size.
	act, est := estimateAction(m, resolveOptions(CompressionOptions{Preset: "balanced", RemoveVideos: true}))
	if act != actRemoveVideo || est != int64(len(placeholderMP4())) {
		t.Fatalf("remove estimate → %q/%d", act, est)
	}
	// A compression level estimates a fraction of the original.
	act, est = estimateAction(m, resolveOptions(CompressionOptions{Preset: "balanced", VideoCompression: "extreme"}))
	if act != actCompressVideo || est <= 0 || est >= m.Bytes {
		t.Fatalf("compress estimate → %q/%d", act, est)
	}
	// No video option → skip, unchanged size.
	act, est = estimateAction(m, resolveOptions(CompressionOptions{Preset: "balanced"}))
	if act != actSkip || est != m.Bytes {
		t.Fatalf("default estimate → %q/%d", act, est)
	}
}

// TestPipelineRemoveVideos runs the full pipeline on a deck containing a large
// fake MP4 and checks the "remove videos" option swaps it for the placeholder.
// No ffmpeg is required for removal.
func TestPipelineRemoveVideos(t *testing.T) {
	video := append(mp4Header("isom"), make([]byte, 1<<20)...) // ~1 MB fake MP4
	path := buildDeck(t, []mediaSpec{
		{name: "ppt/media/media1.mp4", data: video, referenced: true},
	})

	prog := runSync(t, path, CompressionOptions{Preset: "balanced", RemoveVideos: true})
	if prog.State != "done" {
		t.Fatalf("state = %q, errors = %v", prog.State, prog.Errors)
	}

	found := false
	for _, r := range prog.Results {
		if r.PartName == "ppt/media/media1.mp4" {
			found = true
			if r.Action != actRemoveVideo {
				t.Errorf("action = %q, want %q", r.Action, actRemoveVideo)
			}
			if r.AfterBytes != int64(len(placeholderMP4())) {
				t.Errorf("after = %d, want placeholder size %d", r.AfterBytes, len(placeholderMP4()))
			}
		}
	}
	if !found {
		t.Fatal("no result row for the video part")
	}

	// The written output must contain the placeholder under the SAME name.
	out, err := OpenPptx(prog.OutputPath)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	e := out.entry("ppt/media/media1.mp4")
	if e == nil {
		t.Fatal("video part missing from output")
	}
	if len(e.data) != len(placeholderMP4()) {
		t.Errorf("output part size = %d, want %d", len(e.data), len(placeholderMP4()))
	}
}

// TestPipelineCompressVideo exercises the real ffmpeg re-encode path end to
// end. It needs an ffmpeg executable, so it SKIPS when none is installed
// (ffmpeg is also used to synthesize the input test video).
func TestPipelineCompressVideo(t *testing.T) {
	ff, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}

	// Synthesize a 2-second 640×360 test video with visible motion so the
	// "extreme" level has real bytes to shave off.
	src := filepath.Join(t.TempDir(), "in.mp4")
	gen := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=640x360:rate=25",
		"-c:v", "libx264", "-crf", "18", "-pix_fmt", "yuv420p", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate test video: %v: %s", err, out)
	}
	video, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read test video: %v", err)
	}

	path := buildDeck(t, []mediaSpec{
		{name: "ppt/media/media1.mp4", data: video, referenced: true},
	})

	prog := runSync(t, path, CompressionOptions{Preset: "balanced", VideoCompression: "extreme"})
	if prog.State != "done" {
		t.Fatalf("state = %q, errors = %v", prog.State, prog.Errors)
	}
	for _, r := range prog.Results {
		if r.PartName != "ppt/media/media1.mp4" {
			continue
		}
		if r.Action != actCompressVideo {
			t.Fatalf("action = %q (errors %v), want %q", r.Action, prog.Errors, actCompressVideo)
		}
		if r.AfterBytes >= r.BeforeBytes {
			t.Fatalf("video did not shrink: %d → %d", r.BeforeBytes, r.AfterBytes)
		}
		return
	}
	t.Fatal("no result row for the video part")
}

// TestSlideOrderFromPresentation verifies that deck page numbers come from the
// presentation.xml slide list (deck order), not from the slideN.xml file names,
// and that usageLabel renders them as "slide N" / "slides N, M".
func TestSlideOrderFromPresentation(t *testing.T) {
	mk := func(name, data string) *zipEntry { return &zipEntry{name: name, data: []byte(data)} }
	imgRels := func(id, target string) string {
		return `<?xml version="1.0"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="` + id + `" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="` + target + `"/>` +
			`</Relationships>`
	}

	// The deck shows slide2.xml FIRST, then slide1.xml — reordered slides.
	pres := `<?xml version="1.0"?>` +
		`<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"` +
		` xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<p:sldIdLst><p:sldId id="257" r:id="rId3"/><p:sldId id="256" r:id="rId2"/></p:sldIdLst>` +
		`</p:presentation>`
	presRels := `<?xml version="1.0"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>` +
		`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>` +
		`</Relationships>`

	p := &PptxFile{
		ctDefaults:  map[string]string{},
		ctOverrides: map[string]string{},
		relsIndex:   map[string][]relRef{},
		Entries: []*zipEntry{
			mk("ppt/presentation.xml", pres),
			mk("ppt/_rels/presentation.xml.rels", presRels),
			// pic.png is placed on BOTH slides.
			mk("ppt/slides/slide1.xml", `<p:sld><a:blip r:embed="rId1"/></p:sld>`),
			mk("ppt/slides/_rels/slide1.xml.rels", imgRels("rId1", "../media/pic.png")),
			mk("ppt/slides/slide2.xml", `<p:sld><a:blip r:embed="rId1"/></p:sld>`),
			mk("ppt/slides/_rels/slide2.xml.rels", imgRels("rId1", "../media/pic.png")),
			mk("ppt/media/pic.png", "x"),
		},
	}
	if err := p.BuildRelsIndex(); err != nil {
		t.Fatalf("build rels index: %v", err)
	}

	// slide2.xml is deck page 1; slide1.xml is deck page 2.
	if got := p.slidePageNumber("ppt/slides/slide2.xml"); got != 1 {
		t.Errorf("slide2.xml page = %d, want 1", got)
	}
	if got := p.slidePageNumber("ppt/slides/slide1.xml"); got != 2 {
		t.Errorf("slide1.xml page = %d, want 2", got)
	}

	place := p.MediaPlacement("ppt/media/pic.png")
	if !place.UsedOnSlide {
		t.Fatal("pic.png should be used on a slide")
	}
	if got := usageLabel(p.RefCount("ppt/media/pic.png"), place); got != "slides 1, 2" {
		t.Errorf("usage = %q, want %q", got, "slides 1, 2")
	}
}
