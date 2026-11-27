package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/images"
	"github.com/xlsft/harness/packages/client/llm"
)

var (
	fakePNG  = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 1, 2, 3}
	fakeJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 1, 2}
)

type fakeImages struct {
	got  []images.Request
	out  []images.Image
	err  error
	text string
}

func (f *fakeImages) GenerateImages(_ context.Context, r images.Request) (*images.Result, error) {
	f.got = append(f.got, r)
	if f.err != nil {
		return nil, f.err
	}
	out := f.out
	if out == nil {
		for range r.Count {
			out = append(out, images.Image{Data: fakePNG, MediaType: "image/png"})
		}
	}
	return &images.Result{Images: out, Model: r.Model, Priced: true, CostUSD: 0.04, RevisedPrompt: "a red fox", Text: f.text}, nil
}

func imageEnv(t *testing.T) (*Env, string) {
	t.Helper()
	dir := t.TempDir()
	cwd := filepath.Join(dir, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	env := NewEnv(cwd, filepath.Join(dir, "scratch"))
	env.SessionID = "sess-1"
	return env, cwd
}

func runImage(t *testing.T, tool *ImageGenerate, env *Env, in map[string]any) Result {
	t.Helper()
	raw, _ := json.Marshal(in)
	return tool.Run(context.Background(), env, Call{ID: "c1", Input: raw})
}

func imageText(r Result) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func TestImageGenerateWritesInsideTheFolder(t *testing.T) {
	env, cwd := imageEnv(t)
	var snapshots []string
	env.BeforeEditChecked = func(p string) error { snapshots = append(snapshots, p); return nil }
	gen := &fakeImages{}
	tool := &ImageGenerate{Generator: gen, Model: "img-std", ModelName: "Images"}
	out := filepath.Join(cwd, "assets", "fox.png")
	r := runImage(t, tool, env, map[string]any{"prompt": "a fox", "file_path": out, "size": "1024x1024"})
	if r.IsError {
		t.Fatalf("error: %s", imageText(r))
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != string(fakePNG) {
		t.Fatalf("file not written: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0] != out {
		t.Fatalf("snapshots = %v", snapshots)
	}
	if len(r.Content) != 2 || r.Content[1].Type != llm.BlockImage || r.Content[1].Source.MediaType != "image/png" {
		t.Fatalf("content = %+v", r.Content)
	}
	if !strings.Contains(imageText(r), "rewrote the prompt as: a red fox") {
		t.Fatalf("text = %q", imageText(r))
	}
	got := gen.got[0]
	if got.Model != "img-std" || got.Prompt != "a fox" || got.Size != "1024x1024" || got.Count != 1 || got.SessionID != "sess-1" {
		t.Fatalf("request = %+v", got)
	}
	// No temporary file is left beside it.
	entries, _ := os.ReadDir(filepath.Dir(out))
	if len(entries) != 1 {
		t.Fatalf("left files: %v", entries)
	}
}

func TestImageGenerateRefusesOutsideTheFolders(t *testing.T) {
	env, cwd := imageEnv(t)
	gen := &fakeImages{}
	tool := &ImageGenerate{Generator: gen, Model: "img"}
	outside := t.TempDir()
	r := runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(outside, "a.png")})
	if !r.IsError || !strings.Contains(imageText(r), "outside the session's folders") {
		t.Fatalf("outside path allowed: %s", imageText(r))
	}
	// A link inside the folder that points elsewhere does not let a new file out.
	if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
		t.Skip("no symlinks:", err)
	}
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "link", "sub", "a.png")})
	if !r.IsError || !strings.Contains(imageText(r), "outside") {
		t.Fatalf("symlinked path allowed: %s", imageText(r))
	}
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "a.png"), "input_images": []string{filepath.Join(outside, "in.png")}})
	if !r.IsError || !strings.Contains(imageText(r), "outside") {
		t.Fatalf("outside input allowed: %s", imageText(r))
	}
	if len(gen.got) != 0 {
		t.Fatal("the model was called for a refused request")
	}
	// The scratch folder is one of the session's folders.
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(env.ScratchDir, "a.png")})
	if r.IsError {
		t.Fatalf("scratch refused: %s", imageText(r))
	}
}

func TestImageGenerateKeepsExistingFiles(t *testing.T) {
	env, cwd := imageEnv(t)
	tool := &ImageGenerate{Generator: &fakeImages{}, Model: "img"}
	out := filepath.Join(cwd, "a.png")
	_ = os.WriteFile(out, []byte("old"), 0o644)
	r := runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": out})
	if !r.IsError || !strings.Contains(imageText(r), "already exists") {
		t.Fatalf("overwrote without asking: %s", imageText(r))
	}
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": out, "overwrite": true})
	if r.IsError {
		t.Fatal(imageText(r))
	}
	if b, _ := os.ReadFile(out); string(b) != string(fakePNG) {
		t.Fatal("not replaced")
	}
}

func TestImageGenerateSeveralAndFormat(t *testing.T) {
	env, cwd := imageEnv(t)
	gen := &fakeImages{out: []images.Image{{Data: fakeJPEG, MediaType: "image/jpeg"}, {Data: fakePNG, MediaType: "image/png"}}}
	tool := &ImageGenerate{Generator: gen, Model: "img"}
	r := runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "icon.png"), "count": 2})
	if r.IsError {
		t.Fatal(imageText(r))
	}
	// The first came back as JPEG: its extension follows the format.
	for _, name := range []string{"icon-1.jpg", "icon-2.png"} {
		if _, err := os.Stat(filepath.Join(cwd, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !strings.Contains(imageText(r), "extension was changed") {
		t.Fatalf("text = %q", imageText(r))
	}
	files := r.Structured.(map[string]any)["files"].([]map[string]any)
	if len(files) != 2 || files[0]["mediaType"] != "image/jpeg" {
		t.Fatalf("files = %v", files)
	}
	// No extension: the format gives one.
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "plain")})
	if r.IsError {
		t.Fatal(imageText(r))
	}
	if _, err := os.Stat(filepath.Join(cwd, "plain.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestImageGenerateInputsAndErrors(t *testing.T) {
	env, cwd := imageEnv(t)
	gen := &fakeImages{}
	tool := &ImageGenerate{Generator: gen, Model: "img"}
	in := filepath.Join(cwd, "in.jpg")
	_ = os.WriteFile(in, fakeJPEG, 0o644)
	r := runImage(t, tool, env, map[string]any{"prompt": "make it blue", "file_path": filepath.Join(cwd, "out.png"), "input_images": []string{in}})
	if r.IsError {
		t.Fatal(imageText(r))
	}
	if len(gen.got[0].Inputs) != 1 || gen.got[0].Inputs[0].MediaType != "image/jpeg" || gen.got[0].Inputs[0].Name != "in.jpg" {
		t.Fatalf("inputs = %+v", gen.got[0].Inputs)
	}
	text := filepath.Join(cwd, "notes.txt")
	_ = os.WriteFile(text, []byte("hello"), 0o644)
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "o2.png"), "input_images": []string{text}})
	if !r.IsError || !strings.Contains(imageText(r), "not a PNG") {
		t.Fatalf("text input accepted: %s", imageText(r))
	}
	env.ReadDenied = func(p string) bool { return p == in }
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "o3.png"), "input_images": []string{in}})
	if !r.IsError || !strings.Contains(imageText(r), "denied") {
		t.Fatalf("denied input read: %s", imageText(r))
	}
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "o.txt")})
	if !r.IsError || !strings.Contains(imageText(r), ".png") {
		t.Fatalf("text extension accepted: %s", imageText(r))
	}
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "o.png"), "count": 5})
	if !r.IsError {
		t.Fatal("count 5 accepted")
	}
	gen.err = errors.New("rate limited")
	r = runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "o4.png")})
	if !r.IsError || !strings.Contains(imageText(r), "rate limited") {
		t.Fatalf("error = %s", imageText(r))
	}
	if _, err := os.Stat(filepath.Join(cwd, "o4.png")); err == nil {
		t.Fatal("a failed generation wrote a file")
	}
	// Without a model the tool refuses.
	r = runImage(t, &ImageGenerate{}, env, map[string]any{"prompt": "x", "file_path": filepath.Join(cwd, "o5.png")})
	if !r.IsError || !strings.Contains(imageText(r), "Settings > Images") {
		t.Fatalf("unconfigured = %s", imageText(r))
	}
}

func TestImageGenerateRefusesWithoutSnapshot(t *testing.T) {
	env, cwd := imageEnv(t)
	env.BeforeEditChecked = func(string) error { return errors.New("disk full") }
	tool := &ImageGenerate{Generator: &fakeImages{}, Model: "img"}
	out := filepath.Join(cwd, "a.png")
	r := runImage(t, tool, env, map[string]any{"prompt": "x", "file_path": out})
	if !r.IsError || !strings.Contains(imageText(r), "snapshot") {
		t.Fatalf("wrote without a snapshot: %s", imageText(r))
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("file written")
	}
}
