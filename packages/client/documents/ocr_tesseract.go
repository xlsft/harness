package documents

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/tetratelabs/wazero"
	wapi "github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Tesseract 5 and Leptonica, compiled to WebAssembly for WASI by scripts/ocr/build.sh, run under wazero like PDFium.
// The module sees no host files: the trained data is verified here and handed to it as an in-memory, read-only
// directory, and the pixels are copied into its memory. It gets no clock, no environment and no arguments, so a page
// gives the same text every time. Every page runs under a timeout and a memory limit; a page that hits either loses
// the instance, and the next page starts a new one.

//go:embed ocrwasm/tesseract.wasm.gz
var tesseractModuleGz []byte

// tesseractModuleSHA256 is the SHA-256 of the uncompressed module that scripts/ocr/build.sh printed.
//
//go:embed ocrwasm/tesseract.wasm.sha256
var tesseractModuleSHA256 string

const (
	// TesseractVersion and LeptonicaVersion are what scripts/ocr/build.sh pins.
	TesseractVersion = "5.5.4"
	LeptonicaVersion = "1.87.0"
	// OCRPageTimeout bounds the recognition of one page, the start of a new instance included.
	OCRPageTimeout = 2 * time.Minute
	// ocrMemoryPages caps the module's memory: 16384 pages of 64 KiB, 1 GiB.
	ocrMemoryPages = 16384
	// ocrMaxPixels refuses images that would not fit the memory cap with room for Tesseract's own copies.
	ocrMaxPixels = 60_000_000
	// ocrIdle is how long an unused instance keeps its memory before it is dropped.
	ocrIdle = 2 * time.Minute
)

// OCRLanguage is trained data Tesseract can load.
type OCRLanguage struct {
	Code   string // ISO 639-2, as tessdata names it
	Name   string
	Size   int64
	SHA256 string
}

// TessdataBaseURL is where trained data is fetched from: the tessdata_fast repository at its 4.1.0 tag. Each file is
// checked against the SHA-256 below before it is stored or used, so the address only has to serve the right bytes.
var TessdataBaseURL = "https://raw.githubusercontent.com/tesseract-ocr/tessdata_fast/4.1.0/"

// OCRLanguages lists the trained data the client knows (tessdata_fast 4.1.0, Apache-2.0).
var OCRLanguages = map[string]OCRLanguage{
	"eng": {Code: "eng", Name: "English", Size: 4113088, SHA256: "7d4322bd2a7749724879683fc3912cb542f19906c83bcc1a52132556427170b2"},
	"rus": {Code: "rus", Name: "Russian", Size: 3861738, SHA256: "e16e5e036cce1d9ec2b00063cf8b54472625b9e14d893a169e2b0dedeb4df225"},
}

// OCRDefaultLanguages is what recognition uses when the caller names none.
var OCRDefaultLanguages = []string{"eng"}

// OCRCacheDir is where trained data and the compiled module are kept; "" uses the user's cache directory.
var OCRCacheDir = ""

// Tesseract is the built-in OCR engine.
type Tesseract struct{}

func (Tesseract) Name() string { return "Tesseract " + TesseractVersion + " (WebAssembly)" }

func (Tesseract) License() string {
	return "Tesseract Apache-2.0, Leptonica BSD-2-Clause, tessdata_fast Apache-2.0"
}

func init() { OCR = Tesseract{} }

// tess holds the runtime. Its fields belong to whoever holds tessTurn: one page is recognised at a time.
var tess struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mod      wapi.Module
	langs    string       // what mod was initialised with
	data     fstest.MapFS // the module's /tessdata
	idle     *time.Timer
	stderr   bytes.Buffer
}

// tessTurn is held while the module runs. A channel rather than a mutex, so that a caller can give up waiting.
var tessTurn = make(chan struct{}, 1)

// ocrGrace is how long a page may run past its deadline or a cancellation before its caller stops waiting. The module
// checks for cancellation after every word, so this only matters for a page stuck in layout analysis.
const ocrGrace = 15 * time.Second

func ocrCacheDir() string {
	if OCRCacheDir != "" {
		return OCRCacheDir
	}
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "harness", "ocr")
	}
	return ""
}

// NormalizeOCRLanguages checks language codes and puts them in a stable order; nil means the default.
func NormalizeOCRLanguages(langs []string) ([]string, error) {
	if len(langs) == 0 {
		langs = OCRDefaultLanguages
	}
	var out []string
	for _, l := range langs {
		for _, c := range strings.FieldsFunc(strings.ToLower(l), func(r rune) bool { return r == '+' || r == ',' || r == ' ' }) {
			if _, ok := OCRLanguages[c]; !ok {
				known := make([]string, 0, len(OCRLanguages))
				for k := range OCRLanguages {
					known = append(known, k)
				}
				slices.Sort(known)
				return nil, fmt.Errorf("no trained data for language %q; known: %s", c, strings.Join(known, ", "))
			}
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		return OCRDefaultLanguages, nil
	}
	return out, nil
}

// Recognize implements OCREngine.
func (Tesseract) Recognize(ctx context.Context, img image.Image, langs []string) ([]OCRLine, error) {
	langs, err := NormalizeOCRLanguages(langs)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errors.New("the image is empty")
	}
	if b.Dx()*b.Dy() > ocrMaxPixels {
		return nil, fmt.Errorf("the image is %d×%d pixels, over the OCR limit of %d megapixels", b.Dx(), b.Dy(), ocrMaxPixels/1_000_000)
	}
	dpi := 0
	if d, ok := img.(dpiImage); ok {
		dpi, img = d.dpi, d.Image
	}
	data := map[string][]byte{}
	for _, l := range langs {
		d, err := loadTessdata(ctx, l)
		if err != nil {
			return nil, err
		}
		data[l] = d
	}
	grey := greyPixels(img)

	ctx, cancel := context.WithTimeout(ctx, OCRPageTimeout)
	defer cancel()
	select {
	case tessTurn <- struct{}{}:
	case <-ctx.Done():
		return nil, ocrCtxErr(ctx, "waiting for the page before it")
	}
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		defer func() {
			if p := recover(); p != nil {
				r = result{err: fmt.Errorf("the OCR module failed: %v", p)}
			}
			if r.err != nil {
				dropOCRInstance()
			}
			if tess.idle != nil {
				tess.idle.Stop()
			}
			tess.idle = time.AfterFunc(ocrIdle, dropIdleOCR)
			<-tessTurn
			done <- r
		}()
		r.out, r.err = recognizeTurn(ctx, data, grey, b.Dx(), b.Dy(), dpi, strings.Join(langs, "+"))
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		select {
		case r = <-done:
		case <-time.After(ocrGrace):
			// The module keeps the turn until it stops; the next page waits for it or gives up.
			return nil, ocrCtxErr(ctx, "and did not stop when asked")
		}
	}
	if r.err != nil {
		if ctx.Err() != nil {
			return nil, ocrCtxErr(ctx, "")
		}
		return nil, r.err
	}
	return parseOCRResult(r.out), nil
}

func ocrCtxErr(ctx context.Context, detail string) error {
	if detail != "" {
		detail = " " + detail
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("recognition did not finish within %s%s", OCRPageTimeout, detail)
	}
	return fmt.Errorf("recognition was cancelled%s: %v", detail, ctx.Err())
}

// dpiImage carries the resolution a page was rendered at.
type dpiImage struct {
	image.Image
	dpi int
}

// recognizeTurn runs one page; the caller holds tessTurn.
func recognizeTurn(ctx context.Context, data map[string][]byte, grey []byte, w, h, dpi int, langs string) (string, error) {
	if tess.data == nil {
		tess.data = fstest.MapFS{}
	}
	for code, d := range data {
		if name := code + ".traineddata"; tess.data[name] == nil {
			tess.data[name] = &fstest.MapFile{Data: d, Mode: 0o444}
		}
	}
	if err := instantiateOCR(ctx); err != nil {
		return "", err
	}
	m := tess.mod
	tess.stderr.Reset()
	if tess.langs != langs {
		p, err := writeCString(ctx, m, langs)
		if err != nil {
			return "", err
		}
		r, err := m.ExportedFunction("ocr_init").Call(ctx, p)
		freeIn(ctx, m, p)
		if err != nil {
			return "", ocrTrap(err)
		}
		if r[0] != 0 {
			return "", fmt.Errorf("Tesseract could not start: %s", readCString(ctx, m, "ocr_error"))
		}
		tess.langs = langs
	}
	r, err := m.ExportedFunction("ocr_alloc").Call(ctx, uint64(len(grey)))
	if err != nil {
		return "", ocrTrap(err)
	}
	px := r[0]
	if px == 0 || !m.Memory().Write(uint32(px), grey) {
		return "", errors.New("the image does not fit the OCR module's memory")
	}
	defer freeIn(ctx, m, px)
	r, err = m.ExportedFunction("ocr_recognize").Call(ctx, px, uint64(w), uint64(h), uint64(w), uint64(dpi))
	if err != nil {
		return "", ocrTrap(err)
	}
	if r[0] == 0 {
		return "", fmt.Errorf("Tesseract: %s", readCString(ctx, m, "ocr_error"))
	}
	n, err := m.ExportedFunction("ocr_result_len").Call(ctx)
	if err != nil {
		return "", ocrTrap(err)
	}
	out, ok := m.Memory().Read(uint32(r[0]), uint32(n[0]))
	if !ok {
		return "", errors.New("the OCR module returned a result outside its memory")
	}
	return string(out), nil
}

// ocrTrap explains a call that failed inside the module: out of memory or a crash.
func ocrTrap(err error) error {
	msg := strings.TrimSpace(tess.stderr.String())
	if len(msg) > 500 {
		msg = msg[len(msg)-500:]
	}
	if msg != "" {
		return fmt.Errorf("the OCR module stopped: %v (%s)", err, msg)
	}
	return fmt.Errorf("the OCR module stopped: %v", err)
}

// ocrCancelled is the module's import harness.ocr_cancelled: Tesseract asks it after every word. wazero's own
// CloseOnContextDone is not used: it leaves native code at every loop back-edge, which made pages ten times slower.
func ocrCancelled(ctx context.Context) uint32 {
	if ctx.Err() != nil {
		return 1
	}
	return 0
}

func instantiateOCR(ctx context.Context) error {
	if tess.mod != nil && !tess.mod.IsClosed() {
		return nil
	}
	tess.mod, tess.langs = nil, ""
	if tess.rt == nil {
		wasm, err := tesseractModule()
		if err != nil {
			return err
		}
		cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(ocrMemoryPages)
		if dir := ocrCacheDir(); dir != "" {
			dir = filepath.Join(dir, "compiled")
			if err := os.MkdirAll(dir, 0o700); err == nil {
				if cache, err := wazero.NewCompilationCacheWithDir(dir); err == nil {
					cfg = cfg.WithCompilationCache(cache)
				}
			}
		}
		// The runtime and the compiled code live for the process, as PDFium's do; only instances come and go.
		bg := context.WithoutCancel(ctx)
		rt := wazero.NewRuntimeWithConfig(bg, cfg)
		if _, err := wasi_snapshot_preview1.Instantiate(bg, rt); err != nil {
			_ = rt.Close(bg)
			return fmt.Errorf("OCR is not available: %v", err)
		}
		if _, err := rt.NewHostModuleBuilder("harness").NewFunctionBuilder().WithFunc(ocrCancelled).Export("ocr_cancelled").Instantiate(bg); err != nil {
			_ = rt.Close(bg)
			return fmt.Errorf("OCR is not available: %v", err)
		}
		compiled, err := rt.CompileModule(bg, wasm)
		if err != nil {
			_ = rt.Close(bg)
			return fmt.Errorf("OCR is not available: the module does not compile: %v", err)
		}
		tess.rt, tess.compiled = rt, compiled
	}
	// No arguments, no environment, no clock or randomness beyond wazero's deterministic defaults, and one read-only
	// directory holding verified trained data.
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		WithStdout(io.Discard).
		WithStderr(ocrStderr{}).
		WithFSConfig(wazero.NewFSConfig().WithFSMount(tess.data, "/tessdata"))
	mod, err := tess.rt.InstantiateModule(ctx, tess.compiled, cfg)
	if err != nil {
		return fmt.Errorf("the OCR module did not start: %v", err)
	}
	tess.mod = mod
	return nil
}

// dropOCRInstance closes the instance; the caller holds tessTurn.
func dropOCRInstance() {
	if tess.mod != nil {
		_ = tess.mod.Close(context.Background())
	}
	tess.mod, tess.langs = nil, ""
}

// dropIdleOCR gives back an idle instance's memory: linear memory only grows, so a big page would keep it otherwise.
func dropIdleOCR() {
	select {
	case tessTurn <- struct{}{}:
	default:
		return // busy: the page in progress sets a new timer when it ends
	}
	dropOCRInstance()
	<-tessTurn
}

// tesseractModule returns the embedded module, checked against the SHA-256 scripts/ocr/build.sh printed for it.
func tesseractModule() ([]byte, error) {
	if len(tesseractModuleGz) == 0 {
		return nil, ErrNoOCR
	}
	zr, err := gzip.NewReader(bytes.NewReader(tesseractModuleGz))
	if err != nil {
		return nil, fmt.Errorf("the embedded OCR module is damaged: %v", err)
	}
	wasm, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("the embedded OCR module is damaged: %v", err)
	}
	sum := sha256.Sum256(wasm)
	if want := strings.TrimSpace(tesseractModuleSHA256); hex.EncodeToString(sum[:]) != want {
		return nil, fmt.Errorf("the embedded OCR module does not match its checksum %s", want)
	}
	return wasm, nil
}

// tessdataLoaded keeps verified trained data for the life of the process.
var tessdataLoaded = map[string][]byte{}

// loadTessdata returns one language's trained data: from memory, the disk cache, or the network, verified by size and
// SHA-256 every time it comes from outside the process.
func loadTessdata(ctx context.Context, code string) ([]byte, error) {
	ocrFetchMu.Lock()
	defer ocrFetchMu.Unlock()
	if d, ok := tessdataLoaded[code]; ok {
		return d, nil
	}
	lang := OCRLanguages[code]
	cached := ""
	if dir := ocrCacheDir(); dir != "" {
		cached = filepath.Join(dir, "tessdata", code+"-"+lang.SHA256[:16]+".traineddata")
	}
	var data []byte
	if cached != "" {
		if b, err := os.ReadFile(cached); err == nil && verifyTessdata(lang, b) == nil {
			data = b
		}
	}
	if data == nil {
		b, err := fetchTessdata(ctx, lang)
		if err != nil {
			return nil, err
		}
		data = b
		if cached != "" {
			if err := os.MkdirAll(filepath.Dir(cached), 0o700); err == nil {
				_ = WriteAtomic(cached, data, 0o600)
			}
		}
	}
	tessdataLoaded[code] = data
	return data, nil
}

var ocrFetchMu sync.Mutex

func verifyTessdata(lang OCRLanguage, b []byte) error {
	if int64(len(b)) != lang.Size {
		return fmt.Errorf("trained data for %s is %d bytes, expected %d", lang.Name, len(b), lang.Size)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != lang.SHA256 {
		return fmt.Errorf("trained data for %s does not match its checksum (got %s)", lang.Name, got)
	}
	return nil
}

// OCRFetchTimeout bounds one download of trained data.
const OCRFetchTimeout = 2 * time.Minute

func fetchTessdata(ctx context.Context, lang OCRLanguage) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, OCRFetchTimeout)
	defer cancel()
	url := TessdataBaseURL + lang.Code + ".traineddata"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OCR needs the %s trained data (%d MB, fetched once): %v", lang.Name, lang.Size>>20, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OCR needs the %s trained data (%d MB, fetched once): %s answered %s", lang.Name, lang.Size>>20, url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, lang.Size+1))
	if err != nil {
		return nil, fmt.Errorf("fetching the %s trained data: %v", lang.Name, err)
	}
	if err := verifyTessdata(lang, b); err != nil {
		return nil, fmt.Errorf("%v; it was not used", err)
	}
	return b, nil
}

// greyPixels turns an image into 8-bit grey, one byte per pixel, rows without padding, transparent areas white.
func greyPixels(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, w*h)
	switch m := img.(type) {
	case *image.Gray:
		for y := 0; y < h; y++ {
			copy(out[y*w:(y+1)*w], m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):])
		}
		return out
	case *image.RGBA:
		for y := 0; y < h; y++ {
			row := m.Pix[m.PixOffset(b.Min.X, b.Min.Y+y):]
			for x := 0; x < w; x++ {
				p := row[x*4 : x*4+4]
				// Premultiplied: over white, a channel is c + (255 - a).
				out[y*w+x] = luma(uint32(p[0])+255-uint32(p[3]), uint32(p[1])+255-uint32(p[3]), uint32(p[2])+255-uint32(p[3]))
			}
		}
		return out
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA64Model.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA64)
			a := 0xFFFF - uint32(c.A)
			out[y*w+x] = luma((uint32(c.R)+a)>>8, (uint32(c.G)+a)>>8, (uint32(c.B)+a)>>8)
		}
	}
	return out
}

func luma(r, g, b uint32) byte {
	y := (299*r + 587*g + 114*b + 500) / 1000
	return byte(min(y, 255))
}

// parseOCRResult reads the module's rows: confidence, left, top, right, bottom, text.
func parseOCRResult(s string) []OCRLine {
	var lines []OCRLine
	for _, row := range strings.Split(s, "\n") {
		f := strings.SplitN(row, "\t", 6)
		if len(f) != 6 {
			continue
		}
		var n [5]int
		ok := true
		for i := range n {
			v, err := strconv.Atoi(f[i])
			if err != nil {
				ok = false
				break
			}
			n[i] = v
		}
		text := strings.TrimSpace(f[5])
		if !ok || text == "" {
			continue
		}
		lines = append(lines, OCRLine{Text: text, Box: image.Rect(n[1], n[2], n[3], n[4]), Confidence: float64(n[0]) / 100})
	}
	return lines
}

func writeCString(ctx context.Context, m wapi.Module, s string) (uint64, error) {
	r, err := m.ExportedFunction("ocr_alloc").Call(ctx, uint64(len(s)+1))
	if err != nil {
		return 0, ocrTrap(err)
	}
	if r[0] == 0 || !m.Memory().Write(uint32(r[0]), append([]byte(s), 0)) {
		return 0, errors.New("the OCR module is out of memory")
	}
	return r[0], nil
}

func readCString(ctx context.Context, m wapi.Module, fn string) string {
	r, err := m.ExportedFunction(fn).Call(ctx)
	if err != nil || r[0] == 0 {
		return "unknown error"
	}
	mem := m.Memory()
	var b []byte
	for p := uint32(r[0]); len(b) < 4096; p++ {
		c, ok := mem.ReadByte(p)
		if !ok || c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

func freeIn(ctx context.Context, m wapi.Module, p uint64) {
	if !m.IsClosed() {
		_, _ = m.ExportedFunction("ocr_free").Call(ctx, p)
	}
}

// ocrStderr keeps the first 64 KiB the module writes to stderr during a call, for error messages. The module only
// runs while its caller holds tessTurn.
type ocrStderr struct{}

func (ocrStderr) Write(p []byte) (int, error) {
	if room := 64<<10 - tess.stderr.Len(); room > 0 {
		tess.stderr.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
