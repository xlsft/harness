package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpeakableText(t *testing.T) {
	md := "# Results\n\nThe **build** passed, see [the log](https://ci.example/1) and `make test`.\n\n" +
		"```go\nfunc main() {}\n```\n\n- first item\n- [x] done item\n1. step one\n\n| Name | Value |\n|---|---|\n| a | 1 |\n\n> quoted _text_ here\n\n" +
		"![diagram](x.png) Visit https://example.com now<br/>~~old~~ &amp; new"
	got := SpeakableText(md)
	for _, want := range []string{"Results.", "The build passed, see the log and make test.", "(go code block omitted.)", "first item.", "done item.", "1. step one.", "Name, Value.", "a, 1.", "quoted text here.", "Visit now", "old & new."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"**", "```", "func main", "https://", "|", "#", "](", "<br"} {
		if strings.Contains(got, bad) {
			t.Errorf("left %q in:\n%s", bad, got)
		}
	}
	if got := SpeakableText("*one* and *two* words"); got != "one and two words." {
		t.Errorf("emphasis: %q", got)
	}
}

func TestSplitText(t *testing.T) {
	text := "First sentence here. Second sentence follows! Third one? Fourth and last."
	parts := SplitText(text, 30)
	if strings.Join(parts, " ") != text || parts[0] != "First sentence here." {
		t.Fatalf("parts %q", parts)
	}
	for _, p := range parts {
		if len([]rune(p)) > 30 {
			t.Fatalf("too long %q", p)
		}
	}
	if got := SplitText(strings.Repeat("x", 25), 10); len(got) != 3 || got[2] != "xxxxx" {
		t.Fatalf("hard cut %q", got)
	}
}

func sine(rate int, secs float64) PCM {
	n := int(float64(rate) * secs)
	p := PCM{Rate: rate, Samples: make([]int16, n)}
	for i := range p.Samples {
		p.Samples[i] = int16(10000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
	}
	return p
}

func TestWAV(t *testing.T) {
	p := sine(44100, 0.5)
	got, err := DecodeWAV(EncodeWAV(p))
	if err != nil || got.Rate != 44100 || len(got.Samples) != len(p.Samples) || got.Samples[100] != p.Samples[100] {
		t.Fatalf("round trip %v %d", err, len(got.Samples))
	}
	r := got.Resample(16000)
	if r.Rate != 16000 || len(r.Samples) != 8000 || math.Abs(r.Duration()-0.5) > 0.001 {
		t.Fatalf("resample %d %d", r.Rate, len(r.Samples))
	}

	// Stereo float32 with an extra chunk, mixed down.
	var data bytes.Buffer
	for i := 0; i < 100; i++ {
		_ = binary.Write(&data, binary.LittleEndian, float32(0.5))
		_ = binary.Write(&data, binary.LittleEndian, float32(-0.5+float32(i%2)))
	}
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	b.WriteString("LIST")
	_ = binary.Write(&b, binary.LittleEndian, uint32(3))
	b.WriteString("abc\x00")
	b.WriteString("fmt ")
	for _, v := range []any{uint32(16), uint16(3), uint16(2), uint32(8000), uint32(64000), uint16(8), uint16(32)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(0xFFFFFFFF)) // streamed: runs to the end
	b.Write(data.Bytes())
	f, err := DecodeWAV(b.Bytes())
	if err != nil || f.Rate != 8000 || len(f.Samples) != 100 || f.Samples[0] != 0 || f.Samples[1] < 16000 {
		t.Fatalf("float stereo %v %+v", err, f)
	}
	if _, err := DecodeWAV([]byte("OggS....")); !errors.Is(err, ErrNotWAV) {
		t.Fatalf("not wav: %v", err)
	}
}

func TestToPCM16k(t *testing.T) {
	ctx := context.Background()
	none := Tools{LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}
	t.Setenv(EnvFFmpeg, "")
	p, err := none.ToPCM16k(ctx, EncodeWAV(sine(48000, 0.25)), "audio/wav")
	if err != nil || p.Rate != 16000 || len(p.Samples) != 4000 {
		t.Fatalf("wav without ffmpeg: %v %d", err, len(p.Samples))
	}
	if _, err := none.ToPCM16k(ctx, []byte("\x1aE\xdf\xa3webm"), "audio/webm;codecs=opus"); !errors.Is(err, ErrNeedsFFmpeg) {
		t.Fatalf("webm without ffmpeg: %v", err)
	}

	// With a real ffmpeg (when installed): webm/opus as MediaRecorder sends it.
	ff, err := Tools{}.FFmpeg()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.wav"), filepath.Join(dir, "out.webm")
	_ = os.WriteFile(in, EncodeWAV(sine(48000, 1)), 0o600)
	if b, err := exec.Command(ff, "-loglevel", "error", "-i", in, "-c:a", "libopus", out).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode opus: %v %s", err, b)
	}
	webm, _ := os.ReadFile(out)
	p, err = Tools{}.ToPCM16k(ctx, webm, "audio/webm;codecs=opus")
	if err != nil || p.Rate != 16000 || math.Abs(p.Duration()-1) > 0.1 {
		t.Fatalf("webm via ffmpeg: %v %v", err, p.Duration())
	}
}

// tarBz2 builds a .tar.bz2 with the system tar (skips when there is none).
func tarBz2(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()
	src := t.TempDir()
	root := filepath.Join(src, "pkg-1.0")
	for name, content := range files {
		p := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		_ = os.Symlink(target, filepath.Join(root, name))
	}
	out := filepath.Join(t.TempDir(), "a.tar.bz2")
	if b, err := exec.Command("tar", "-cjf", out, "-C", src, "pkg-1.0").CombinedOutput(); err != nil {
		t.Skipf("tar -j unavailable: %v %s", err, b)
	}
	b, _ := os.ReadFile(out)
	return b
}

func testCatalog(base string) []LocalModel {
	rt := LocalModel{Provider: ProviderRuntime, ID: "sherpa-onnx", DisplayName: "runtime", Required: []string{SherpaOffline, SherpaOfflineTTS},
		Files: []RemoteFile{{URL: base + "/runtime.tar.bz2", Archive: true, Size: 100, Keep: keepRuntime}}}
	p := parakeetModel("p1", "x", "Parakeet", "", 100, nil, true)
	p.Files[0].URL = base + "/parakeet.tar.bz2"
	v := piperVoice("en_US-test-medium", "test voice", 100, true)
	v.Files[0].URL = base + "/voice.tar.bz2"
	return []LocalModel{rt, p, v}
}

func TestModelDownloads(t *testing.T) {
	runtimeArchive := tarBz2(t, map[string]string{
		SherpaOffline: "#!/bin/sh\n", SherpaOfflineTTS: "#!/bin/sh\n", "bin/other-tool": "x", "lib/libonnxruntime.1.2.dylib": "lib", "include/c-api.h": "h",
	}, map[string]string{"lib/libonnxruntime.dylib": "libonnxruntime.1.2.dylib"})
	parakeetArchive := tarBz2(t, map[string]string{
		"encoder.int8.onnx": "e", "decoder.int8.onnx": "d", "joiner.int8.onnx": "j", "tokens.txt": "t", "encoder.onnx": "fp32", "test_wavs/0.wav": "w",
	}, nil)
	var gate = make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/runtime.tar.bz2":
			_, _ = w.Write(runtimeArchive)
		case "/parakeet.tar.bz2":
			<-gate
			_, _ = w.Write(parakeetArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewModels(dir, testCatalog(srv.URL), srv.Client())
	ctx := context.Background()

	if _, err := m.Start(ctx, "nope/x"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown: %v", err)
	}
	d, err := m.Start(ctx, "parakeet/p1")
	if err != nil {
		t.Fatal(err)
	}
	// A second caller joins the running download.
	if d2, _ := m.Start(ctx, "parakeet/p1"); d2 != d || m.Downloading("parakeet/p1") == nil {
		t.Fatal("not joined")
	}
	close(gate)
	var seen []int64
	pollInterval = 5 * time.Millisecond
	if err := d.Watch(ctx, func(n, total int64) error { seen = append(seen, total); return nil }); err != nil {
		t.Fatalf("download: %v", err)
	}
	if seen[0] != 200 {
		t.Fatalf("the total includes the runtime: %v", seen)
	}
	p, _ := m.Find("parakeet/p1")
	rt, _ := m.Find(RuntimeKey)
	if !m.Ready(p) {
		t.Fatal("not ready")
	}
	// Only the files the engine needs are kept; the runtime keeps its tools, libraries and their links.
	entries, _ := os.ReadDir(m.Path(p))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"decoder.int8.onnx", "encoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"}) {
		t.Fatalf("model files %v", names)
	}
	st, err := os.Stat(filepath.Join(m.Path(rt), SherpaOffline))
	if err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("runtime tool %v %v", err, st)
	}
	if target, err := os.Readlink(filepath.Join(m.Path(rt), "lib", "libonnxruntime.dylib")); err != nil || target != "libonnxruntime.1.2.dylib" {
		t.Fatalf("symlink %q %v", target, err)
	}
	for _, skipped := range []string{"bin/other-tool", "include/c-api.h"} {
		if _, err := os.Stat(filepath.Join(m.Path(rt), skipped)); err == nil {
			t.Fatalf("%s extracted", skipped)
		}
	}
	// Installed: done at once, no request.
	before := hits.Load()
	d, _ = m.Start(ctx, "parakeet/p1")
	<-d.Done()
	if hits.Load() != before || d.Err() != nil {
		t.Fatal("re-downloaded")
	}
	states := m.States(KindSTT)
	if len(states) != 1 || !states[0].Installed {
		t.Fatalf("states %+v", states)
	}

	// A failed download leaves nothing behind.
	d, _ = m.Start(ctx, "piper/en_US-test-medium")
	<-d.Done()
	if d.Err() == nil || !strings.Contains(d.Err().Error(), "HTTP 404") {
		t.Fatalf("404: %v", d.Err())
	}
	if left, _ := os.ReadDir(filepath.Join(dir, ProviderPiper)); len(left) != 0 {
		t.Fatalf("leftovers %v", left)
	}

	cancelled, err := m.Delete("parakeet/p1")
	if err != nil || cancelled {
		t.Fatalf("delete %v %v", cancelled, err)
	}
	if ok, _ := m.Installed(p); ok {
		t.Fatal("still installed")
	}
}

func TestLocalEngines(t *testing.T) {
	dir := t.TempDir()
	m := NewModels(dir, testCatalog("http://unused"), nil)
	install := func(x LocalModel) {
		for _, f := range x.Required {
			p := filepath.Join(m.Path(x), filepath.FromSlash(f))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte("x"), 0o755)
		}
	}
	var calls [][]string
	l := &Local{Models: m, GOOS: "darwin", Tools: Tools{LookPath: func(n string) (string, error) {
		if n == "say" {
			return "/usr/bin/say", nil
		}
		return "", exec.ErrNotFound
	}}}
	l.Exec = func(_ context.Context, bin string, args, env []string) ([]byte, error) {
		calls = append(calls, append([]string{bin}, args...))
		switch filepath.Base(bin) {
		case "sherpa-onnx-offline":
			return []byte("Started\n/tmp/audio.wav\n{\"lang\": \"\", \"text\": \" hello from parakeet\", \"timestamps\": []}\nnum threads: 4\n"), nil
		case "sherpa-onnx-offline-tts", "say":
			for _, a := range args {
				if v, ok := strings.CutPrefix(a, "--output-filename="); ok {
					_ = os.WriteFile(v, EncodeWAV(sine(22050, 0.1)), 0o600)
				}
			}
			if i := slices.Index(args, "-o"); i >= 0 {
				_ = os.WriteFile(args[i+1], EncodeWAV(sine(22050, 0.1)), 0o600)
			}
			if slices.Equal(args, []string{"-v", "?"}) {
				return []byte("Albert              en_US    # Hello! My name is Albert.\nEddy (English (UK)) en_GB    # Hello! My name is Eddy.\nbogus line\n"), nil
			}
		}
		return nil, nil
	}
	ctx := context.Background()

	// Nothing installed: unavailable with reasons.
	if _, err := l.Transcribe(ctx, ProviderParakeet, "", sine(16000, 0.1), ""); !errors.Is(err, ErrLocalUnavailable) {
		t.Fatalf("no model: %v", err)
	}
	ps := l.LocalProviders(ctx, 0)
	if len(ps) != 4 || ps[0].GetId() != ProviderParakeet || ps[0].GetAvailable() || ps[0].GetUnavailableReason() == "" || ps[3].GetId() != ProviderSay || !ps[3].GetAvailable() {
		t.Fatalf("providers %+v", ps)
	}
	if v := ps[3].GetVoices(); len(v) != 2 || v[1].GetId() != "Eddy (English (UK))" || v[1].GetLanguage() != "en-GB" {
		t.Fatalf("say voices %+v", v)
	}
	if !slices.Contains(ps[0].GetInputMimeTypes(), "audio/wav") || slices.Contains(ps[0].GetInputMimeTypes(), "audio/webm") {
		t.Fatalf("mime types without ffmpeg %v", ps[0].GetInputMimeTypes())
	}

	rt, _ := m.Find(RuntimeKey)
	p, _ := m.Find("parakeet/p1")
	v, _ := m.Find("piper/en_US-test-medium")
	install(p)
	if _, err := l.Transcribe(ctx, ProviderParakeet, "", sine(16000, 0.1), ""); !errors.Is(err, ErrLocalUnavailable) {
		t.Fatalf("no runtime: %v", err)
	}
	install(rt)
	install(v)
	r, err := l.Transcribe(ctx, ProviderParakeet, "", sine(48000, 0.5), "en")
	if err != nil || r.Text != "hello from parakeet" || r.Model != "p1" || math.Abs(r.Duration-0.5) > 0.01 {
		t.Fatalf("transcribe %v %+v", err, r)
	}
	last := calls[len(calls)-1]
	if !slices.Contains(last, "--model-type=nemo_transducer") || !strings.HasSuffix(last[len(last)-1], "audio.wav") || !strings.HasSuffix(last[0], SherpaOffline) {
		t.Fatalf("args %v", last)
	}

	out, err := l.Synthesize(ctx, ProviderPiper, "", "- Hello there. "+strings.Repeat("More words here. ", 200), 2)
	if err != nil || out.Voice != "en_US-test-medium" {
		t.Fatalf("piper %v %+v", err, out)
	}
	if wav, err := DecodeWAV(out.WAV); err != nil || wav.Duration() < 0.19 {
		t.Fatalf("piper wav: %v (pieces are joined)", err)
	}
	last = calls[len(calls)-1]
	if !slices.Contains(last, "--vits-length-scale=0.500") || strings.HasPrefix(last[len(last)-1], "-") {
		t.Fatalf("piper args %v", last)
	}
	if out, err := l.Synthesize(ctx, ProviderSay, "Albert", "Hi", 1); err != nil || len(out.WAV) == 0 {
		t.Fatalf("say %v", err)
	}
	if last = calls[len(calls)-1]; last[1] != "-v" || last[2] != "Albert" || !slices.Contains(last, "175") {
		t.Fatalf("say args %v", last)
	}
	ps = l.LocalProviders(ctx, KindTTS)
	if !ps[0].GetAvailable() || ps[0].GetDefaultVoice() != "en_US-test-medium" || !ps[0].GetVoices()[0].GetInstalled() {
		t.Fatalf("piper provider %+v", ps[0])
	}

	l.GOOS = "linux"
	if _, err := l.Synthesize(ctx, ProviderSay, "", "Hi", 1); !errors.Is(err, ErrLocalUnavailable) {
		t.Fatalf("say on linux: %v", err)
	}
}

func TestRuntimeAssets(t *testing.T) {
	for _, p := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
		if a, size := runtimeAsset(p[0], p[1]); a == "" || size == 0 {
			t.Errorf("no runtime for %v", p)
		}
	}
	if a, _ := runtimeAsset("freebsd", "amd64"); a != "" {
		t.Error("freebsd")
	}
}
