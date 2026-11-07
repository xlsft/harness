package documents

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	wapi "github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Pages are rasterised by PDFium compiled to WebAssembly, so builds stay free of CGO. The module has no access to the
// file system: it gets the document's bytes and returns pixels. It compiles on first use, which takes a while, so the
// compiled code is cached on disk, and the runtime stays up for the life of the process.

const (
	// OverviewDPI and DetailDPI are what an overview and a requested page render at.
	OverviewDPI = 36
	DetailDPI   = 110
	// OverviewPages is how many pages one overview image shows.
	OverviewPages = 12
	// overviewColumns is the width of the overview grid.
	overviewColumns = 4
	// rasterTimeout bounds one render, a first compile included.
	rasterTimeout = 3 * time.Minute
	// maxPageSidePx refuses a page that would render absurdly large.
	maxPageSidePx = 4000
)

// rasterizer owns the WebAssembly runtime.
var rasterizer struct {
	sync.Mutex
	pool pdfium.Pool
	dir  string
}

// RasterCacheDir is where the compiled module is kept; "" uses the user's cache directory.
var RasterCacheDir = ""

func rasterPool() (pdfium.Pool, error) {
	rasterizer.Lock()
	defer rasterizer.Unlock()
	if rasterizer.pool != nil {
		return rasterizer.pool, nil
	}
	cfg := wazero.NewRuntimeConfig().WithCoreFeatures(wapi.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling)
	dir := RasterCacheDir
	if dir == "" {
		if base, err := os.UserCacheDir(); err == nil {
			dir = filepath.Join(base, "harness", "pdfium")
		}
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			if cache, err := wazero.NewCompilationCacheWithDir(dir); err == nil {
				cfg = cfg.WithCompilationCache(cache)
			}
		}
	}
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle: 0, MaxIdle: 1, MaxTotal: 1,
		RuntimeConfig: cfg,
		FSConfig:      wazero.NewFSConfig(), // no host files: the document comes as bytes
		Stdout:        io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("PDF rendering is not available: %v", err)
	}
	rasterizer.pool = pool
	return pool, nil
}

// closeRaster throws the runtime away, after a page hung it.
func closeRaster() {
	rasterizer.Lock()
	defer rasterizer.Unlock()
	if rasterizer.pool != nil {
		_ = rasterizer.pool.Close()
		rasterizer.pool = nil
	}
}

// RenderPages rasterises the given 1-based pages. A hostile page is bounded by a timeout, and a panic becomes an error.
func RenderPages(ctx context.Context, pdf []byte, pages []int, dpi int) ([]image.Image, error) {
	type result struct {
		imgs []image.Image
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{nil, fmt.Errorf("the page could not be rendered: %v", r)}
			}
		}()
		imgs, err := renderPages(pdf, pages, dpi)
		done <- result{imgs, err}
	}()
	timer := time.NewTimer(rasterTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.imgs, r.err
	case <-timer.C:
		closeRaster()
		return nil, fmt.Errorf("rendering did not finish within %s", rasterTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func renderPages(pdf []byte, pages []int, dpi int) ([]image.Image, error) {
	pool, err := rasterPool()
	if err != nil {
		return nil, err
	}
	inst, err := pool.GetInstance(rasterTimeout)
	if err != nil {
		return nil, fmt.Errorf("PDF rendering is not available: %v", err)
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		return nil, fmt.Errorf("PDFium cannot open it: %v", err)
	}
	defer func() { _, _ = inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document}) }()
	var out []image.Image
	for _, p := range pages {
		size, err := inst.GetPageSizeInPixels(&requests.GetPageSizeInPixels{DPI: dpi, Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: p - 1}}})
		if err != nil {
			return nil, fmt.Errorf("page %d: %v", p, err)
		}
		use := dpi
		if size.Width > maxPageSidePx || size.Height > maxPageSidePx {
			use = dpi * maxPageSidePx / max(size.Width, size.Height)
		}
		r, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{DPI: use, Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: p - 1}}})
		if err != nil {
			return nil, fmt.Errorf("page %d: %v", p, err)
		}
		// The pixels live in the module's memory: copy them out over white.
		src := r.Result.Image
		b := src.Bounds()
		dst := image.NewRGBA(b)
		draw.Draw(dst, b, image.White, image.Point{}, draw.Src)
		draw.Draw(dst, b, src, b.Min, draw.Over)
		r.Cleanup()
		out = append(out, dst)
	}
	return out, nil
}

// Overview lays pages out in a grid with their numbers, as one image.
func Overview(imgs []image.Image, numbers []int) image.Image {
	cellW, cellH := 1, 1
	for _, m := range imgs {
		cellW, cellH = max(cellW, m.Bounds().Dx()), max(cellH, m.Bounds().Dy())
	}
	cols := min(overviewColumns, len(imgs))
	rows := (len(imgs) + cols - 1) / cols
	const gap, label = 6, 14
	canvas := image.NewRGBA(image.Rect(0, 0, cols*(cellW+gap)+gap, rows*(cellH+gap+label)+gap))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{0xCC, 0xCC, 0xCC, 0xFF}), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: canvas, Src: image.Black, Face: basicfont.Face7x13}
	for i, m := range imgs {
		x := gap + (i%cols)*(cellW+gap)
		y := gap + (i/cols)*(cellH+gap+label)
		d.Dot = fixed.P(x, y+10)
		d.DrawString(strconv.Itoa(numbers[i]))
		r := image.Rect(x, y+label, x+m.Bounds().Dx(), y+label+m.Bounds().Dy())
		draw.Draw(canvas, r, m, m.Bounds().Min, draw.Src)
	}
	return canvas
}

// EncodePNG writes an image as PNG.
func EncodePNG(m image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
