package browser

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRecordingWritesAGIF(t *testing.T) {
	rec := &recording{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	rec.cancel = cancel
	seen := make(chan struct{}, 8)
	calls := 0
	go rec.capture(ctx, func(context.Context) ([]byte, error) {
		calls++
		seen <- struct{}{}
		if calls == 2 {
			return nil, errors.New("the tab went away") // one bad frame doesn't end it
		}
		return pngOf(t, 1440, 900, color.RGBA{R: 200, A: 255}), nil
	}, time.Millisecond)
	// The frame of a call counts once the next call started.
	for range 4 {
		<-seen
	}
	cancel()
	<-rec.done
	if len(rec.frames) < 2 {
		t.Fatalf("%d frames", len(rec.frames))
	}
	if b := rec.frames[0].Bounds(); b.Dx() != recordMaxWidth || b.Dy() != 450 {
		t.Fatalf("frame %v", b)
	}
	path := filepath.Join(t.TempDir(), "a", "r.gif")
	if err := writeGIF(path, rec.frames, recordEvery); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Image) != len(rec.frames) || got.Delay[0] != 100 {
		t.Fatalf("%d images, delay %d", len(got.Image), got.Delay[0])
	}
}

func TestRecordingStopsAtTheLimit(t *testing.T) {
	rec := &recording{done: make(chan struct{})}
	frame := pngOf(t, 10, 10, color.White)
	rec.capture(context.Background(), func(context.Context) ([]byte, error) { return frame, nil }, time.Microsecond)
	if len(rec.frames) != recordMaxFrames {
		t.Fatalf("%d frames", len(rec.frames))
	}
}

func TestOneRecordingPerSession(t *testing.T) {
	var r recorders
	if !r.start("s", &recording{}) {
		t.Fatal("first")
	}
	if r.start("s", &recording{}) {
		t.Fatal("a second one started")
	}
	if !r.start("t", &recording{}) {
		t.Fatal("another session")
	}
	if r.take("s") == nil || r.take("s") != nil {
		t.Fatal("take")
	}
}
