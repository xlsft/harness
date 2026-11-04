package computeruse

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // Decode reads the drivers' PNGs.
)

// maxImageSide bounds screenshots sent to the model, which downscales larger ones itself and would make the pixels it
// sees differ from the ones it names.
const maxImageSide = 1568

// encodeFrame scales a captured image to fit maxSide and encodes it as JPEG. It returns the new size.
func encodeFrame(data []byte, maxSide int) ([]byte, int, int, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode screenshot: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("empty screenshot")
	}
	dw, dh := w, h
	if w >= h && w > maxSide {
		dw, dh = maxSide, max(1, h*maxSide/w)
	} else if h > w && h > maxSide {
		dw, dh = max(1, w*maxSide/h), maxSide
	}
	var out image.Image = src
	if dw != w || dh != h {
		out = boxScale(src, dw, dh)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 82}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode screenshot: %w", err)
	}
	return buf.Bytes(), dw, dh, nil
}

// boxScale downscales by averaging the source pixels under each target pixel, which keeps small text readable.
func boxScale(src image.Image, dw, dh int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := range dw {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), uint8(a / n >> 8)})
		}
	}
	return dst
}
