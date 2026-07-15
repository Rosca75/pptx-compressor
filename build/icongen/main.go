// Command icongen renders the PPTX Compressor app icon.
//
// It draws the logo once at 1024x1024 with an anti-aliased, pure-Go vector
// rasterizer (golang.org/x/image/vector), then emits:
//
//   - appicon.png       1024x1024 master (Linux build + Wails default source)
//   - icon.ico          multi-resolution Windows icon (16..256, PNG-embedded)
//
// The design mirrors build/appicon.svg exactly, in the same 0..1024 coordinate
// space, so the SVG stays the human-editable source of truth and this program
// stays the deterministic rasterizer. No CGo, no external image binaries.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// kappa is the magic constant that turns four cubic-bezier segments into a
// near-perfect circle; used for the rounded-rectangle corners.
const kappa = 0.5522847498307936

func main() {
	// Render the master logo at full resolution.
	master := renderLogo(1024)

	// 1) Write the 1024x1024 PNG master.
	writePNG("appicon.png", master)

	// 2) Write the multi-resolution Windows .ico. Explorer, the taskbar and the
	//    window title bar pick whichever embedded size they need. 256 is the
	//    "large icons" view; 16/32/48 cover taskbar and small views.
	writeICO("icon.ico", master, []int{256, 128, 64, 48, 32, 24, 16})

	log.Println("wrote appicon.png and icon.ico")
}

// renderLogo draws the whole icon at the requested square size and returns it.
// Everything is expressed in the 1024-unit design space and scaled by s/1024.
func renderLogo(size int) *image.RGBA {
	const design = 1024.0
	scale := float32(size) / design
	dst := image.NewRGBA(image.Rect(0, 0, size, size))

	// --- Background: PowerPoint-orange rounded square with a diagonal gradient.
	// Build a gradient source image, then paint it through the rounded-rect mask.
	grad := diagonalGradient(size,
		color.RGBA{0xEA, 0x5A, 0x2D, 0xFF}, // warm orange (top-left)
		color.RGBA{0xC0, 0x34, 0x1A, 0xFF}) // deep orange (bottom-right)
	bg := vector.NewRasterizer(size, size)
	addRoundRect(bg, 40*scale, 40*scale, 944*scale, 944*scale, 210*scale)
	bg.Draw(dst, dst.Bounds(), grad, image.Point{})

	// --- White presentation "slide" card centred on the orange field.
	slide := vector.NewRasterizer(size, size)
	addRoundRect(slide, 252*scale, 317*scale, 520*scale, 390*scale, 30*scale)
	slide.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{})

	// --- Two orange block arrows converging vertically = "compress the slide".
	arrowColor := image.NewUniform(color.RGBA{0xD2, 0x47, 0x26, 0xFF})

	// Top arrow, pointing DOWN. Vertices match the SVG path exactly.
	top := vector.NewRasterizer(size, size)
	poly(top, scale,
		467, 360, 557, 360, 557, 408, 597, 408,
		512, 498, 427, 408, 467, 408)
	top.Draw(dst, dst.Bounds(), arrowColor, image.Point{})

	// Bottom arrow, pointing UP.
	bot := vector.NewRasterizer(size, size)
	poly(bot, scale,
		467, 664, 557, 664, 557, 616, 597, 616,
		512, 526, 427, 616, 467, 616)
	bot.Draw(dst, dst.Bounds(), arrowColor, image.Point{})

	return dst
}

// addRoundRect appends a closed rounded-rectangle contour to ra. Corners are
// drawn as cubic beziers (via kappa) so they read as true circular arcs.
func addRoundRect(ra *vector.Rasterizer, x, y, w, h, r float32) {
	k := r * kappa
	x2, y2 := x+w, y+h

	ra.MoveTo(x+r, y)
	ra.LineTo(x2-r, y)
	ra.CubeTo(x2-r+k, y, x2, y+r-k, x2, y+r) // top-right
	ra.LineTo(x2, y2-r)
	ra.CubeTo(x2, y2-r+k, x2-r+k, y2, x2-r, y2) // bottom-right
	ra.LineTo(x+r, y2)
	ra.CubeTo(x+r-k, y2, x, y2-r+k, x, y2-r) // bottom-left
	ra.LineTo(x, y+r)
	ra.CubeTo(x, y+r-k, x+r-k, y, x+r, y) // top-left
	ra.ClosePath()
}

// poly appends a closed polygon from a flat list of x,y pairs (in design units,
// scaled by s). Used for the block arrows.
func poly(ra *vector.Rasterizer, s float32, pts ...float32) {
	ra.MoveTo(pts[0]*s, pts[1]*s)
	for i := 2; i < len(pts); i += 2 {
		ra.LineTo(pts[i]*s, pts[i+1]*s)
	}
	ra.ClosePath()
}

// diagonalGradient returns an opaque size×size image whose colour interpolates
// linearly from c0 at the top-left corner to c1 at the bottom-right corner.
func diagonalGradient(size int, c0, c1 color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	den := float64(2 * (size - 1))
	if den == 0 {
		den = 1
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			t := float64(x+y) / den
			img.SetRGBA(x, y, color.RGBA{
				R: lerp(c0.R, c1.R, t),
				G: lerp(c0.G, c1.G, t),
				B: lerp(c0.B, c1.B, t),
				A: 0xFF,
			})
		}
	}
	return img
}

// lerp linearly interpolates one 8-bit channel between a and b at t in [0,1].
func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5)
}

// resize produces a high-quality downscaled copy using CatmullRom, the same
// filter the compressor uses for image resizing.
func resize(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

// writePNG encodes img to path as PNG.
func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
}

// writeICO builds a Windows .ico containing one PNG-compressed entry per size.
// PNG-in-ICO is supported by every Windows version this app targets (10/11).
func writeICO(path string, master *image.RGBA, sizes []int) {
	// Encode each requested size to PNG bytes (re-scaled from the master).
	type entry struct {
		size int
		data []byte
	}
	var entries []entry
	for _, s := range sizes {
		img := master
		if s != master.Bounds().Dx() {
			img = resize(master, s)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			log.Fatal(err)
		}
		entries = append(entries, entry{s, buf.Bytes()})
	}

	var out bytes.Buffer
	// ICONDIR: reserved(0), type(1=icon), count.
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(len(entries)))

	// Image data starts right after the directory entries (16 bytes each).
	offset := 6 + 16*len(entries)
	for _, e := range entries {
		// bWidth/bHeight: 0 means 256.
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0
		}
		out.WriteByte(dim)                                           // width
		out.WriteByte(dim)                                           // height
		out.WriteByte(0)                                             // color count (0 = truecolor)
		out.WriteByte(0)                                             // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))           // color planes
		binary.Write(&out, binary.LittleEndian, uint16(32))          // bits per pixel
		binary.Write(&out, binary.LittleEndian, uint32(len(e.data))) // bytes in resource
		binary.Write(&out, binary.LittleEndian, uint32(offset))      // offset to data
		offset += len(e.data)
	}
	for _, e := range entries {
		out.Write(e.data)
	}

	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
