// =============================================================================
// video.go — Video detection, the "remove videos" placeholder, and MP4
//            recompression through an external ffmpeg executable.
// =============================================================================
//
// Videos embedded in a deck live under ppt/media/ exactly like images, but the
// image pipeline never touches them (detectFormat classifies them as fmtMedia
// and the decision matrix skips fmtMedia). This file adds two video-specific
// features on top of that:
//
//  1. REMOVE VIDEOS — replace each video part's bytes with a tiny (~2 KB)
//     valid MP4 placeholder. This mirrors how the per-image "remove" override
//     works for pictures (a 1×1 pixel): the part, its relationships and its
//     content type all stay in place, so the package structure cannot break —
//     only the bytes shrink. Deleting the part outright would require surgery
//     on every slide's XML (shape, relationships, timing tree) and any missed
//     edit corrupts the file, so we deliberately do not do that.
//
//  2. COMPRESS MP4 — re-encode MP4 videos through ffmpeg at one of four
//     levels. CGo is forbidden in this project and there is no pure-Go H.264
//     encoder, so we shell out to an ffmpeg EXECUTABLE (looked up next to the
//     app, then on PATH). This is optional at runtime: when ffmpeg is not
//     installed the UI disables the option, and a run without ffmpeg simply
//     keeps the original video (never-larger rule still holds).
// =============================================================================

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// =============================================================================
// Detection — which media parts are videos, and which are MP4?
// =============================================================================

// videoExtensions lists file extensions (lower-case, no dot) that PowerPoint
// commonly uses for embedded video parts. Used as a fallback when the bytes
// alone are ambiguous (e.g. an unrecognised container).
var videoExtensions = map[string]bool{
	"mp4": true, "m4v": true, "mov": true, "avi": true, "wmv": true,
	"mpg": true, "mpeg": true, "webm": true, "mkv": true, "asf": true,
}

// audioOnlyBrands lists ISO-BMFF "ftyp" major brands that mean the file is an
// audio container (e.g. .m4a), NOT a video. detectFormat lumps both under
// fmtMedia; here we need to tell them apart so "remove videos" never touches
// narration audio.
var audioOnlyBrands = map[string]bool{
	"M4A ": true, "M4B ": true, "M4P ": true,
}

// isMP4Data reports whether data is an ISO-BMFF (MP4/MOV family) VIDEO file:
// it starts with a size + "ftyp" box and its major brand is not audio-only.
// This is the input gate for ffmpeg recompression — we only re-encode MP4s.
func isMP4Data(data []byte) bool {
	if len(data) < 12 || !bytes.Equal(data[4:8], []byte("ftyp")) {
		return false
	}
	brand := string(data[8:12])
	return !audioOnlyBrands[brand]
}

// isVideoPart reports whether the media part looks like a video, deciding from
// the bytes first (magic numbers) and the file extension as a fallback.
func isVideoPart(name string, data []byte) bool {
	// ISO-BMFF (mp4/mov/m4v) — but not audio-only brands like M4A.
	if isMP4Data(data) {
		return true
	}
	// AVI: "RIFF" .... "AVI ".
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("AVI ")) {
		return true
	}
	// ASF/WMV: the ASF header GUID 30 26 B2 75 8E 66 CF 11.
	if bytes.HasPrefix(data, []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}) {
		return true
	}
	// Matroska / WebM: EBML magic 1A 45 DF A3.
	if bytes.HasPrefix(data, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return true
	}
	// MPEG program stream / MPEG-1: 00 00 01 BA.
	if bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0xBA}) {
		return true
	}
	// Fallback: trust a well-known video extension.
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	return videoExtensions[ext]
}

// =============================================================================
// Placeholder — a tiny valid MP4 used by the "remove videos" option
// =============================================================================

// placeholderMP4B64 is a ~2 KB MP4 (one 16×16 black frame + 0.1 s of silent
// AAC audio, H.264 baseline, faststart) generated once with ffmpeg and
// embedded here so the app needs no runtime dependency to remove videos.
// Substituting these bytes for a large video keeps every relationship, content
// type and slide shape valid — the poster frame keeps showing on the slide.
const placeholderMP4B64 = "" +
	"AAAAIGZ0eXBpc29tAAACAGlzb21pc28yYXZjMW1wNDEAAAVTbW9vdgAAAGxtdmhkAAAAAAAAAAAA" +
	"AAAAAAAD6AAAAGQAAQAAAQAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAAAAAAAAAAAAAA" +
	"AABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAwAAAjl0cmFrAAAAXHRraGQAAAADAAAA" +
	"AAAAAAAAAAABAAAAAAAAAGQAAAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAABAAAAAAAA" +
	"AAAAAAAAAABAAAAAABAAAAAQAAAAAAAkZWR0cwAAABxlbHN0AAAAAAAAAAEAAABkAAAAAAABAAAA" +
	"AAGxbWRpYQAAACBtZGhkAAAAAAAAAAAAAAAAAAAoAAAABABVxAAAAAAALWhkbHIAAAAAAAAAAHZp" +
	"ZGUAAAAAAAAAAAAAAABWaWRlb0hhbmRsZXIAAAABXG1pbmYAAAAUdm1oZAAAAAEAAAAAAAAAAAAA" +
	"ACRkaW5mAAAAHGRyZWYAAAAAAAAAAQAAAAx1cmwgAAAAAQAAARxzdGJsAAAAuHN0c2QAAAAAAAAA" +
	"AQAAAKhhdmMxAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAAAABAAEABIAAAASAAAAAAAAAABFExhdmM2" +
	"MS4zLjEwMCBsaWJ4MjY0AAAAAAAAAAAAAAAAGP//AAAALmF2Y0MBQsAK/+EAFmdCwArZHsBEAAAD" +
	"AAQAAAMAUDxImSABAAVoy4PLIAAAABBwYXNwAAAAAQAAAAEAAAAUYnRydAAAAAAAAMjwAADI8AAA" +
	"ABhzdHRzAAAAAAAAAAEAAAABAAAEAAAAABxzdHNjAAAAAAAAAAEAAAABAAAAAQAAAAEAAAAUc3Rz" +
	"egAAAAAAAAKDAAAAAQAAABRzdGNvAAAAAAAAAAEAAAWWAAACRXRyYWsAAABcdGtoZAAAAAMAAAAA" +
	"AAAAAAAAAAIAAAAAAAAAZAAAAAAAAAAAAAAAAQEAAAAAAQAAAAAAAAAAAAAAAAAAAAEAAAAAAAAA" +
	"AAAAAAAAAEAAAAAAAAAAAAAAAAAAACRlZHRzAAAAHGVsc3QAAAAAAAAAAQAAAGQAAAQAAAEAAAAA" +
	"Ab1tZGlhAAAAIG1kaGQAAAAAAAAAAAAAAAAAAB9AAAAHIFXEAAAAAAAtaGRscgAAAAAAAAAAc291" +
	"bgAAAAAAAAAAAAAAAFNvdW5kSGFuZGxlcgAAAAFobWluZgAAABBzbWhkAAAAAAAAAAAAAAAkZGlu" +
	"ZgAAABxkcmVmAAAAAAAAAAEAAAAMdXJsIAAAAAEAAAEsc3RibAAAAH5zdHNkAAAAAAAAAAEAAABu" +
	"bXA0YQAAAAAAAAABAAAAAAAAAAAAAQAQAAAAAB9AAAAAAAA2ZXNkcwAAAAADgICAJQACAASAgIAX" +
	"QBUAAAAAAB9AAAADJwWAgIAFFYhW5QAGgICAAQIAAAAUYnRydAAAAAAAAB9AAAADJwAAACBzdHRz" +
	"AAAAAAAAAAIAAAABAAAEAAAAAAEAAAMgAAAAHHN0c2MAAAAAAAAAAQAAAAEAAAABAAAAAQAAABxz" +
	"dHN6AAAAAAAAAAAAAAACAAAAEwAAAAQAAAAYc3RjbwAAAAAAAAACAAAFgwAACBkAAAAac2dwZAEA" +
	"AAByb2xsAAAAAgAAAAH//wAAABxzYmdwAAAAAHJvbGwAAAABAAAAAgAAAAEAAABhdWR0YQAAAFlt" +
	"ZXRhAAAAAAAAACFoZGxyAAAAAAAAAABtZGlyYXBwbAAAAAAAAAAAAAAAACxpbHN0AAAAJKl0b28A" +
	"AAAcZGF0YQAAAAEAAAAATGF2ZjYxLjEuMTAwAAAACGZyZWUAAAKibWRhdNwATGF2YzYxLjMuMTAw" +
	"AAIwQA4AAAJxBgX//23cRem95tlIt5Ys2CDZI+7veDI2NCAtIGNvcmUgMTY0IHIzMTkxIDQ2MTNh" +
	"YzMgLSBILjI2NC9NUEVHLTQgQVZDIGNvZGVjIC0gQ29weWxlZnQgMjAwMy0yMDI0IC0gaHR0cDov" +
	"L3d3dy52aWRlb2xhbi5vcmcveDI2NC5odG1sIC0gb3B0aW9uczogY2FiYWM9MCByZWY9MyBkZWJs" +
	"b2NrPTE6MDowIGFuYWx5c2U9MHgxOjB4MTExIG1lPWhleCBzdWJtZT03IHBzeT0xIHBzeV9yZD0x" +
	"LjAwOjAuMDAgbWl4ZWRfcmVmPTEgbWVfcmFuZ2U9MTYgY2hyb21hX21lPTEgdHJlbGxpcz0xIDh4" +
	"OGRjdD0wIGNxbT0wIGRlYWR6b25lPTIxLDExIGZhc3RfcHNraXA9MSBjaHJvbWFfcXBfb2Zmc2V0" +
	"PS0yIHRocmVhZHM9MSBsb29rYWhlYWRfdGhyZWFkcz0xIHNsaWNlZF90aHJlYWRzPTAgbnI9MCBk" +
	"ZWNpbWF0ZT0xIGludGVybGFjZWQ9MCBibHVyYXlfY29tcGF0PTAgY29uc3RyYWluZWRfaW50cmE9" +
	"MCBiZnJhbWVzPTAgd2VpZ2h0cD0wIGtleWludD0yNTAga2V5aW50X21pbj0xMCBzY2VuZWN1dD00" +
	"MCBpbnRyYV9yZWZyZXNoPTAgcmNfbG9va2FoZWFkPTQwIHJjPWNyZiBtYnRyZWU9MSBjcmY9MjMu" +
	"MCBxY29tcD0wLjYwIHFwbWluPTAgcXBtYXg9NjkgcXBzdGVwPTQgaXBfcmF0aW89MS40MCBhcT0x" +
	"OjEuMDAAgAAAAApliIQN8mKAALb+ARggBw=="

// placeholderMP4 returns the decoded placeholder bytes. Decoding happens once
// (sync.Once) and the result is shared — callers must not mutate it.
var placeholderMP4 = sync.OnceValue(func() []byte {
	b, err := base64.StdEncoding.DecodeString(placeholderMP4B64)
	if err != nil {
		// The constant above is fixed at compile time; a decode error is a
		// programming mistake, not a runtime condition.
		panic(fmt.Sprintf("placeholder mp4 is corrupt: %v", err))
	}
	return b
})

// =============================================================================
// Compression levels — the four ffmpeg profiles offered in the UI
// =============================================================================

// videoLevel bundles the ffmpeg knobs for one compression level. CRF (constant
// rate factor) is x264's quality dial: higher = smaller file, lower quality.
type videoLevel struct {
	// crf is the x264 constant-rate-factor (18 ≈ visually lossless, 36 ≈ rough).
	crf int

	// maxEdge caps the video's longest dimension in pixels (0 = keep as-is).
	// Slides rarely need more than ~1280 px of video.
	maxEdge int

	// fps caps the frame rate (0 = keep the source frame rate).
	fps int

	// audioBitrate is the AAC audio bitrate, e.g. "96k".
	audioBitrate string

	// estFactor is the rough output/input size ratio used for the analyzer's
	// savings ESTIMATE only (the real number comes from the actual encode).
	estFactor float64
}

// videoLevels maps the level name sent by the frontend to its ffmpeg profile.
// The four levels the UI offers, from gentlest to harshest.
var videoLevels = map[string]videoLevel{
	"light":      {crf: 23, maxEdge: 1920, fps: 0, audioBitrate: "128k", estFactor: 0.70},
	"balanced":   {crf: 28, maxEdge: 1280, fps: 0, audioBitrate: "96k", estFactor: 0.45},
	"aggressive": {crf: 32, maxEdge: 960, fps: 30, audioBitrate: "64k", estFactor: 0.30},
	"extreme":    {crf: 36, maxEdge: 640, fps: 24, audioBitrate: "48k", estFactor: 0.15},
}

// videoLevelFor returns the profile for a level name and whether it exists.
// "" and "none" mean video compression is off.
func videoLevelFor(name string) (videoLevel, bool) {
	lv, ok := videoLevels[strings.ToLower(strings.TrimSpace(name))]
	return lv, ok
}

// =============================================================================
// ffmpeg discovery — find the executable next to the app or on PATH
// =============================================================================

// ffmpegPath locates an ffmpeg executable. It prefers a copy sitting next to
// the app binary (so users can just drop ffmpeg.exe into the app folder), and
// falls back to the system PATH. Returns an error when neither exists.
func ffmpegPath() (string, error) {
	// exeName is "ffmpeg.exe" on Windows, "ffmpeg" elsewhere.
	exeName := "ffmpeg" + exeSuffix

	// 1) Next to our own executable.
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), exeName)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}

	// 2) On PATH.
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("ffmpeg not found (place ffmpeg%s next to the app or install it on PATH)", exeSuffix)
}

// ffmpegAvailable reports whether an ffmpeg executable can be located. Used by
// analysis so the UI can enable/disable the video-compression option.
func ffmpegAvailable() bool {
	_, err := ffmpegPath()
	return err == nil
}

// =============================================================================
// compressVideoMP4 — re-encode one MP4 through ffmpeg
// =============================================================================

// compressVideoMP4 re-encodes MP4 bytes at the given level and returns the new
// bytes. ffmpeg only works on files, so the input is written to a temp file,
// ffmpeg writes a temp output, and both are deleted afterwards. The ctx lets
// a Cancel press kill a long-running encode. The caller enforces the
// never-larger rule on the returned bytes.
func compressVideoMP4(ctx context.Context, data []byte, levelName string) ([]byte, error) {
	lv, ok := videoLevelFor(levelName)
	if !ok {
		return nil, fmt.Errorf("unknown video compression level %q", levelName)
	}

	ff, err := ffmpegPath()
	if err != nil {
		return nil, err
	}

	// Write the input to a temp file ffmpeg can read.
	in, err := os.CreateTemp("", "pptxvid-in-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("temp input: %w", err)
	}
	inPath := in.Name()
	defer os.Remove(inPath)
	if _, err := in.Write(data); err != nil {
		in.Close()
		return nil, fmt.Errorf("temp input write: %w", err)
	}
	in.Close()

	// Reserve an output temp path (ffmpeg creates the actual file with -y).
	out, err := os.CreateTemp("", "pptxvid-out-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("temp output: %w", err)
	}
	outPath := out.Name()
	out.Close()
	defer os.Remove(outPath)

	// Build the ffmpeg command line for this level.
	//
	//   -crf N              x264 quality dial for this level
	//   -preset medium      speed/size balance (the default sweet spot)
	//   -vf scale=...       downscale so the LONGEST edge is capped, keeping the
	//                       aspect ratio; the "if(gte(iw,ih),...)" expression
	//                       picks which axis to cap and min() never upscales.
	//                       "-2" makes ffmpeg compute the other axis rounded to
	//                       an even number (required by H.264).
	//   -r N                frame-rate cap (harsher levels only)
	//   -c:a aac -b:a N     re-encode audio at the level's bitrate
	//   -movflags +faststart  put the index at the front so playback can start
	//                       before the whole file is read
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", inPath,
		"-c:v", "libx264",
		"-preset", "medium",
		"-crf", strconv.Itoa(lv.crf),
		"-pix_fmt", "yuv420p",
	}
	if lv.maxEdge > 0 {
		me := strconv.Itoa(lv.maxEdge)
		scale := "scale='if(gte(iw,ih),min(" + me + ",iw),-2)':'if(gte(iw,ih),-2,min(" + me + ",ih))'"
		args = append(args, "-vf", scale)
	}
	if lv.fps > 0 {
		args = append(args, "-r", strconv.Itoa(lv.fps))
	}
	args = append(args,
		"-c:a", "aac", "-b:a", lv.audioBitrate,
		"-movflags", "+faststart",
		outPath,
	)

	cmd := exec.CommandContext(ctx, ff, args...)
	hideConsoleWindow(cmd) // Windows: don't flash a console window per encode
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Include ffmpeg's own error text — it names the actual problem
		// (missing codec, truncated file, ...) far better than the exit code.
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("ffmpeg failed: %v: %s", err, msg)
	}

	outData, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read ffmpeg output: %w", err)
	}
	if len(outData) == 0 {
		return nil, fmt.Errorf("ffmpeg produced an empty file")
	}
	return outData, nil
}
