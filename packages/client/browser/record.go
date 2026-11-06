package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	_ "image/jpeg" // screenshots come as JPEG or PNG
	_ "image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/tools"
)

// ToolRecord records what the session's browser shows into an animated GIF, to hand a person.
const ToolRecord = "BrowserRecord"

const (
	// recordEvery is how often a frame is taken; recordMaxFrames ends a recording, so a forgotten one stays small.
	recordEvery     = time.Second
	recordMaxFrames = 120
	// recordMaxWidth shrinks frames: this is for seeing what happened, and a frame costs its pixels in memory.
	recordMaxWidth = 720
)

// recording is one running capture.
type recording struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	frames []*image.Paletted
	failed error
}

// Recorders hold the running capture of each session (the tool is stateless, a browser may restart).
type recorders struct {
	mu sync.Mutex
	m  map[string]*recording
}

func (r *recorders) start(session string, rec *recording) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*recording{}
	}
	if _, running := r.m[session]; running {
		return false
	}
	r.m[session] = rec
	return true
}

func (r *recorders) take(session string) *recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.m[session]
	delete(r.m, session)
	return rec
}

var sessionRecorders recorders

// shot takes one picture: the browser's Screenshot, or a stub in tests.
type shot func(ctx context.Context) ([]byte, error)

// capture adds frames until ctx ends or the limit is reached. The first frame comes at once.
func (rec *recording) capture(ctx context.Context, take shot, every time.Duration) {
	defer close(rec.done)
	t := time.NewTicker(every)
	defer t.Stop()
	// Attempts count too, so that a browser that went away with the session doesn't keep this going.
	for range recordMaxFrames {
		data, err := take(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			var f *image.Paletted
			if f, err = gifFrame(data); err == nil {
				rec.mu.Lock()
				rec.frames = append(rec.frames, f)
				full := len(rec.frames) >= recordMaxFrames
				rec.mu.Unlock()
				if full {
					return
				}
			}
		}
		if err != nil {
			rec.mu.Lock()
			rec.failed = err
			rec.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// gifFrame decodes a screenshot into a frame: shrunk, on a fixed palette. All frames share the palette so the file stays
// small, and a page's colors come out close enough to tell what happened.
func gifFrame(data []byte) (*image.Paletted, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("read the screenshot: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > recordMaxWidth {
		h = max(h*recordMaxWidth/w, 1)
		w = recordMaxWidth
	}
	out := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
	for y := range h {
		sy := b.Min.Y + y*b.Dy()/h
		row := out.Pix[y*out.Stride : y*out.Stride+w]
		for x := range row {
			r, g, bl, _ := src.At(b.Min.X+x*b.Dx()/w, sy).RGBA()
			row[x] = paletteIndex(r>>11, g>>11, bl>>11)
		}
	}
	return out, nil
}

// planTable holds the nearest palette entry of every color at 5 bits a channel: looking each pixel up in the palette takes a
// second a frame, and this runs once a second beside the session's work.
var planTable = sync.OnceValue(func() *[1 << 15]uint8 {
	var t [1 << 15]uint8
	p := color.Palette(palette.Plan9)
	for i := range t {
		c := color.RGBA{R: uint8(i>>10) << 3, G: uint8(i>>5&31) << 3, B: uint8(i&31) << 3, A: 255}
		t[i] = uint8(p.Index(c))
	}
	return &t
})

func paletteIndex(r, g, b uint32) uint8 { return planTable()[r<<10|g<<5|b] }

// writeGIF encodes frames, each shown for as long as they were apart.
func writeGIF(path string, frames []*image.Paletted, every time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	delay := int(every / (10 * time.Millisecond))
	g := &gif.GIF{}
	for _, fr := range frames {
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, delay)
	}
	if err := gif.EncodeAll(f, g); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

type recordTool struct{ toolBase }

func (*recordTool) Name() string { return ToolRecord }

// ReadOnly: it only looks at the page, and writes to the session's scratch directory.
func (*recordTool) ReadOnly(json.RawMessage) bool { return true }

func (t *recordTool) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	var in struct {
		Action string `json:"action"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return tools.ErrorResult("%v", err)
	}
	if env == nil || env.SessionID == "" {
		return tools.ErrorResult("the browser needs a session")
	}
	switch in.Action {
	case "start":
		b, err := t.browser(env)
		if err != nil {
			return errResult(err)
		}
		if err := b.Ensure(ctx); err != nil {
			return errResult(err)
		}
		// It outlives this call, and ends with stop or at its limit.
		life, cancel := context.WithCancel(context.WithoutCancel(ctx))
		rec := &recording{cancel: cancel, done: make(chan struct{})}
		if !sessionRecorders.start(env.SessionID, rec) {
			cancel()
			return tools.ErrorResult("a recording is running already: stop it first")
		}
		go rec.capture(life, func(ctx context.Context) ([]byte, error) {
			img, err := b.Screenshot(ctx, ScreenshotOptions{})
			return img.Data, err
		}, recordEvery)
		r := tools.TextResult(fmt.Sprintf("Recording the browser, one frame a second, at most %d frames. Stop it with action stop.", recordMaxFrames), map[string]any{"recording": true})
		r.Summary = "Recording"
		return r
	case "stop":
		rec := sessionRecorders.take(env.SessionID)
		if rec == nil {
			return tools.ErrorResult("no recording is running")
		}
		rec.cancel()
		<-rec.done
		rec.mu.Lock()
		frames, failed := rec.frames, rec.failed
		rec.mu.Unlock()
		if len(frames) == 0 {
			if failed != nil {
				return tools.ErrorResult("nothing was recorded: %v", failed)
			}
			return tools.ErrorResult("nothing was recorded: it stopped before the first frame")
		}
		path := filepath.Join(env.ScratchDir, "recordings", time.Now().UTC().Format("20060102-150405")+".gif")
		if err := writeGIF(path, frames, recordEvery); err != nil {
			return tools.ErrorResult("could not write the recording: %v", err)
		}
		text := fmt.Sprintf("Recorded %d frames to %s. SendUserFile hands it to the user.", len(frames), path)
		if len(frames) >= recordMaxFrames {
			text += " It reached the limit and stopped before you did."
		}
		r := tools.TextResult(text, map[string]any{"path": path, "frames": len(frames)})
		r.Summary = fmt.Sprintf("%d frames", len(frames))
		return r
	}
	return tools.ErrorResult("InputValidationError: action must be start or stop")
}
