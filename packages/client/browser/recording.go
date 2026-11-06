package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"time"
)

// Recording a task as a GIF: the screencast of the active tab, kept while the recording runs and encoded when it stops. A
// page only sends frames when it changes, so a frame's delay is the time until the next one. The frames stay in memory as the
// JPEGs Chrome sent; both limits stop the recording rather than the session when it runs too long.
const (
	recordingMaxFrames   = 900
	recordingMaxBytes    = 300 << 20
	recordingMaxDuration = 10 * time.Minute
	recordingFrameGap    = 150 * time.Millisecond // about 6 frames a second
	recordingMaxWidth    = 960
	recordingLastDelay   = time.Second
)

// Recording is a finished recording.
type Recording struct {
	Path     string
	Frames   int
	Size     int64
	Duration time.Duration
}

type recorder struct {
	started time.Time
	frames  []recordedFrame
	size    int
	last    time.Time
	full    bool
}

type recordedFrame struct {
	at  time.Time
	jpg []byte
}

// offer takes a screencast frame, skipping those too close to the last one.
func (r *recorder) offer(img []byte, now time.Time) {
	if r.full {
		return
	}
	if len(r.frames) > 0 && now.Sub(r.last) < recordingFrameGap {
		return
	}
	if len(r.frames) >= recordingMaxFrames || r.size+len(img) > recordingMaxBytes || now.Sub(r.started) > recordingMaxDuration {
		r.full = true
		return
	}
	r.frames = append(r.frames, recordedFrame{at: now, jpg: img})
	r.size += len(img)
	r.last = now
}

// StartRecording starts recording the active tab. The first frame is taken at once: a page that does not change sends none.
func (b *Browser) StartRecording(ctx context.Context) error {
	t, c, err := b.activeTab(ctx)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.rec != nil {
		b.mu.Unlock()
		return errors.New("a recording is already running")
	}
	rec := &recorder{started: time.Now()}
	b.rec = rec
	b.lastRecording = ""
	b.notifyLocked(true)
	b.mu.Unlock()
	var shot struct {
		Data string `json:"data"`
	}
	if err := c.Call(ctx, t.session, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 70}, &shot); err == nil {
		if img, err := base64.StdEncoding.DecodeString(shot.Data); err == nil {
			b.mu.Lock()
			rec.offer(img, time.Now())
			b.mu.Unlock()
		}
	}
	b.syncScreencast()
	return nil
}

// StopRecording ends the recording and writes it to dir as a GIF.
func (b *Browser) StopRecording(dir string) (Recording, error) {
	b.mu.Lock()
	rec := b.rec
	b.rec = nil
	b.notifyLocked(true)
	b.mu.Unlock()
	if rec == nil {
		return Recording{}, errors.New("no recording is running")
	}
	b.syncScreencast()
	if len(rec.frames) == 0 {
		return Recording{}, errors.New("nothing was recorded")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Recording{}, err
	}
	path := filepath.Join(dir, "browser-"+rec.started.UTC().Format("20060102-150405")+".gif")
	f, err := os.CreateTemp(dir, "recording-*.tmp")
	if err != nil {
		return Recording{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := encodeGIF(f, rec.frames); err != nil {
		return Recording{}, fmt.Errorf("encoding the recording: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		return Recording{}, err
	}
	if err := f.Close(); err != nil {
		return Recording{}, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return Recording{}, err
	}
	b.mu.Lock()
	b.lastRecording = path
	b.notifyLocked(true)
	b.mu.Unlock()
	return Recording{Path: path, Frames: len(rec.frames), Size: st.Size(), Duration: rec.frames[len(rec.frames)-1].at.Sub(rec.frames[0].at)}, nil
}

// Recording reports whether a recording runs.
func (b *Browser) Recording() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rec != nil
}

type writer interface {
	Write([]byte) (int, error)
}

// encodeGIF writes the frames as a looping GIF. A frame draws only the rectangle that changed, over the one before, which keeps
// a recording of a mostly still page small.
func encodeGIF(w writer, frames []recordedFrame) error {
	out := &gif.GIF{LoopCount: 0}
	var prev *image.RGBA
	var bounds image.Rectangle
	for i, f := range frames {
		img, err := jpeg.Decode(bytes.NewReader(f.jpg))
		if err != nil {
			continue // A frame Chrome sent broken is not worth failing over.
		}
		cur := scaled(img, bounds)
		if prev == nil {
			bounds = cur.Bounds()
		}
		delay := recordingLastDelay
		if i+1 < len(frames) {
			delay = frames[i+1].at.Sub(f.at)
		}
		hundredths := int(delay / (10 * time.Millisecond))
		if hundredths < 2 {
			hundredths = 2
		}
		rect := bounds
		if prev != nil {
			rect = changed(prev, cur)
			if rect.Empty() {
				// Nothing moved: the frame before just lasts longer.
				if n := len(out.Delay); n > 0 {
					out.Delay[n-1] += hundredths
				}
				continue
			}
		}
		p := image.NewPaletted(rect, palette.Plan9)
		draw.FloydSteinberg.Draw(p, rect, cur, rect.Min)
		out.Image = append(out.Image, p)
		out.Delay = append(out.Delay, hundredths)
		out.Disposal = append(out.Disposal, gif.DisposalNone)
		prev = cur
	}
	if len(out.Image) == 0 {
		return errors.New("no frame could be read")
	}
	out.Config = image.Config{ColorModel: color.Palette(palette.Plan9), Width: bounds.Dx(), Height: bounds.Dy()}
	return gif.EncodeAll(w, out)
}

// scaled draws img at the recording's size: at most recordingMaxWidth wide, and for later frames exactly the first's, since
// a GIF has one canvas. Nearest neighbour: this runs per frame, and text stays sharp enough to read.
func scaled(img image.Image, canvas image.Rectangle) *image.RGBA {
	src := img.Bounds()
	if canvas.Empty() {
		w, h := src.Dx(), src.Dy()
		if w > recordingMaxWidth {
			h = h * recordingMaxWidth / w
			w = recordingMaxWidth
		}
		canvas = image.Rect(0, 0, w, h)
	}
	dst := image.NewRGBA(canvas)
	for y := 0; y < canvas.Dy(); y++ {
		sy := src.Min.Y + y*src.Dy()/canvas.Dy()
		for x := 0; x < canvas.Dx(); x++ {
			dst.Set(x, y, img.At(src.Min.X+x*src.Dx()/canvas.Dx(), sy))
		}
	}
	return dst
}

// changed returns the bounding box of the pixels that differ noticeably: JPEG noise is not a change.
func changed(a, b *image.RGBA) image.Rectangle {
	const tolerance = 12
	r := image.Rectangle{}
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		ai, bi := a.PixOffset(bounds.Min.X, y), b.PixOffset(bounds.Min.X, y)
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			d := abs(int(a.Pix[ai])-int(b.Pix[bi])) + abs(int(a.Pix[ai+1])-int(b.Pix[bi+1])) + abs(int(a.Pix[ai+2])-int(b.Pix[bi+2]))
			if d > tolerance {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
			ai += 4
			bi += 4
		}
	}
	return r
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
