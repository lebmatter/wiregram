// Package halfblock draws images in the terminal with the "▄" half-block:
// each cell packs two pixel rows, the top as a truecolor background and the
// bottom as the foreground.
package halfblock

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// maxPixels bounds what Load decodes, so a tiny file claiming huge
// dimensions cannot exhaust memory.
const maxPixels = 50_000_000

// Load decodes the image at path and scales it down to at most maxWidth
// pixels wide, keeping only a small copy in memory.
func Load(path string, maxWidth int) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("read image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("image is %dx%d, too large to draw", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind image: %w", err)
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	b := img.Bounds()
	if b.Dx() <= maxWidth {
		return img, nil
	}
	h := max(1, b.Dy()*maxWidth/b.Dx())
	return scale(img, maxWidth, h), nil
}

// Size fits img into at most maxCols by maxRows cells without upscaling and
// keeping its aspect ratio; zero means it cannot be drawn.
func Size(img image.Image, maxCols, maxRows int) (cols, rows int) {
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || maxCols <= 0 || maxRows <= 0 {
		return 0, 0
	}
	cols = min(maxCols, b.Dx())
	rows = (b.Dy()*cols/b.Dx() + 1) / 2
	if rows > maxRows {
		rows = maxRows
		cols = min(cols, max(1, b.Dx()*rows*2/b.Dy()))
	}
	return cols, max(1, rows)
}

// Render draws img as rows lines of cols cells.
func Render(img image.Image, cols, rows int) []string {
	dst := scale(img, cols, rows*2)
	lines := make([]string, rows)
	for y := range rows {
		var line strings.Builder
		for x := range cols {
			top := dst.RGBAAt(x, 2*y)
			bottom := dst.RGBAAt(x, 2*y+1)
			fmt.Fprintf(&line, "\x1b[48;2;%d;%d;%dm\x1b[38;2;%d;%d;%dm▄",
				top.R, top.G, top.B, bottom.R, bottom.G, bottom.B)
		}
		line.WriteString("\x1b[0m")
		lines[y] = line.String()
	}
	return lines
}

func scale(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}
