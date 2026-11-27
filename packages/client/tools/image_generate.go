package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/documents"
	"github.com/xlsft/harness/packages/client/images"
	"github.com/xlsft/harness/packages/client/llm"
)

// ImageGenerate makes images with the user's image model (docs/design/images.md) and writes them into the session's
// folders. It is a write tool: the permission check sees file_path as Write's, and the tool itself refuses any path
// outside the working directories and the session's scratch folder, whatever the answer was.
type ImageGenerate struct {
	// Generator serves the requests; nil: the session has no image model, and the tool refuses (it is not offered then).
	Generator images.Generator
	// Model is the image model id, ModelName what to call it.
	Model     string
	ModelName string
}

type imageGenerateInput struct {
	Prompt      string   `json:"prompt"`
	FilePath    string   `json:"file_path"`
	Size        string   `json:"size"`
	Count       int      `json:"count"`
	InputImages []string `json:"input_images"`
	Quality     string   `json:"quality"`
	Overwrite   bool     `json:"overwrite"`
}

// ImageInputMaxBytes bounds one input image (the server's limit).
const ImageInputMaxBytes = 20 << 20

func (*ImageGenerate) Name() string                  { return "ImageGenerate" }
func (*ImageGenerate) ReadOnly(json.RawMessage) bool { return false }

var imageExts = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif"}

// evalExisting resolves the symlinks of a path whose last parts may not exist yet: those of its deepest existing
// ancestor, so a new file under a link to elsewhere is judged where it would land.
func evalExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// imageFolders are where the tool may read and write: the working directories and the session's scratch folder.
func imageFolders(env *Env) []string {
	dirs := env.WorkingDirs()
	if env.ScratchDir != "" {
		dirs = append(dirs, env.ScratchDir)
	}
	return dirs
}

func insideFolders(path string, dirs []string) bool {
	path = evalExisting(path)
	for _, d := range dirs {
		if d == "" {
			continue
		}
		rel, err := filepath.Rel(evalExisting(d), path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// imageTargets are the files of count images at path: path itself for one, "name-1.png", "name-2.png"... for several.
func imageTargets(path string, count int) []string {
	if count == 1 {
		return []string{path}
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	out := make([]string, count)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d%s", stem, i+1, ext)
	}
	return out
}

// withImageExt gives path the extension of mediaType when it has none or one of another format.
func withImageExt(path, mediaType string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != "" && imageExts[ext] == mediaType {
		return path
	}
	if _, isImage := imageExts[ext]; isImage {
		path = strings.TrimSuffix(path, filepath.Ext(path))
	}
	return path + images.Extension(mediaType)
}

func checkTarget(path string, overwrite bool) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if !overwrite {
		return fmt.Errorf("%s already exists; pass overwrite: true to replace it, or choose another file_path", path)
	}
	return nil
}

func (t *ImageGenerate) Run(ctx context.Context, env *Env, call Call) Result {
	if t.Generator == nil || t.Model == "" {
		return ErrorResult("No image model is set up for this user: an image model is chosen in Settings > Images.")
	}
	var in imageGenerateInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" || in.FilePath == "" {
		return ErrorResult("InputValidationError: prompt and file_path are required")
	}
	if !filepath.IsAbs(in.FilePath) {
		return ErrorResult("file_path must be an absolute path, got %q", in.FilePath)
	}
	if in.Count == 0 {
		in.Count = 1
	}
	if in.Count < 1 || in.Count > images.MaxCount {
		return ErrorResult("count must be between 1 and %d", images.MaxCount)
	}
	path := filepath.Clean(in.FilePath)
	if ext := strings.ToLower(filepath.Ext(path)); ext != "" {
		if _, ok := imageExts[ext]; !ok {
			return ErrorResult("file_path must end in .png, .jpg, .jpeg, .webp or .gif, or have no extension (the format then follows the image), not %s", ext)
		}
	}
	dirs := imageFolders(env)
	if !insideFolders(path, dirs) {
		return ErrorResult("%s is outside the session's folders (%s); images are written only inside them", path, strings.Join(env.WorkingDirs(), ", "))
	}
	targets := imageTargets(path, in.Count)
	for _, p := range targets {
		if err := checkTarget(p, in.Overwrite); err != nil {
			return ErrorResult("%v", err)
		}
	}
	var inputs []images.Input
	for _, p := range in.InputImages {
		if !filepath.IsAbs(p) {
			return ErrorResult("input_images must be absolute paths, got %q", p)
		}
		p = filepath.Clean(p)
		if !insideFolders(p, dirs) {
			return ErrorResult("%s is outside the session's folders; input images are read only from inside them", p)
		}
		if env.ReadDenied != nil && env.ReadDenied(p) {
			return ErrorResult("Reading %s is denied by a permission rule", p)
		}
		st, err := os.Stat(p)
		if err != nil {
			return ErrorResult("%v", err)
		}
		if st.Size() > ImageInputMaxBytes {
			return ErrorResult("%s is too large (%d bytes, max %d)", p, st.Size(), ImageInputMaxBytes)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return ErrorResult("%v", err)
		}
		mt := images.Sniff(data)
		if mt != "image/png" && mt != "image/jpeg" && mt != "image/webp" {
			return ErrorResult("%s is not a PNG, JPEG or WebP image", p)
		}
		inputs = append(inputs, images.Input{Data: data, MediaType: mt, Name: filepath.Base(p)})
	}
	if ctx.Err() != nil {
		return ErrorResult("interrupted")
	}
	res, err := t.Generator.GenerateImages(ctx, images.Request{Model: t.Model, Prompt: in.Prompt, Size: strings.TrimSpace(in.Size), Quality: strings.TrimSpace(in.Quality),
		Count: in.Count, Inputs: inputs, SessionID: env.SessionID, AgentID: env.AgentID})
	if err != nil {
		if ctx.Err() != nil {
			return ErrorResult("interrupted")
		}
		return ErrorResult("Image generation failed: %v", err)
	}
	if len(res.Images) == 0 {
		return ErrorResult("The image model returned no image")
	}
	got := res.Images
	if len(got) > len(targets) {
		got = got[:len(targets)]
	}
	// Where each image goes, with the extension of its format; nothing is written until every target is fine.
	written := make([]string, len(got))
	var renamed []string
	for i, img := range got {
		mt := images.Sniff(img.Data)
		if mt == "" {
			return ErrorResult("The image model returned something that is not an image")
		}
		got[i].MediaType = mt
		p := withImageExt(targets[i], mt)
		if p != targets[i] {
			if err := checkTarget(p, in.Overwrite); err != nil {
				return ErrorResult("%v", err)
			}
			if !insideFolders(p, dirs) {
				return ErrorResult("%s is outside the session's folders", p)
			}
			renamed = append(renamed, fmt.Sprintf("%s (the image is %s)", filepath.Base(p), strings.TrimPrefix(mt, "image/")))
		}
		written[i] = p
	}
	files := make([]map[string]any, 0, len(got))
	for i, img := range got {
		p := written[i]
		if err := env.beforeEditChecked(p); err != nil {
			return ErrorResult("No rewind snapshot of %s could be taken, so it was not written: %v", p, err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return ErrorResult("%v", err)
		}
		if err := documents.WriteAtomic(p, img.Data, 0o644); err != nil {
			return ErrorResult("writing %s: %v", p, err)
		}
		env.Files.MarkRead(p)
		files = append(files, map[string]any{"filePath": p, "mediaType": img.MediaType, "bytes": len(img.Data)})
	}

	model := t.ModelName
	if model == "" {
		model = t.Model
	}
	var b strings.Builder
	if len(written) == 1 {
		fmt.Fprintf(&b, "Generated 1 image with %s and wrote it to %s.", model, written[0])
	} else {
		fmt.Fprintf(&b, "Generated %d images with %s and wrote them to:\n%s", len(written), model, strings.Join(written, "\n"))
	}
	if len(res.Images) < in.Count {
		fmt.Fprintf(&b, "\nThe model returned %d of the %d images asked for.", len(res.Images), in.Count)
	}
	if len(renamed) > 0 {
		fmt.Fprintf(&b, "\nThe extension was changed to match the format: %s.", strings.Join(renamed, ", "))
	}
	if res.RevisedPrompt != "" && res.RevisedPrompt != in.Prompt {
		fmt.Fprintf(&b, "\nThe model rewrote the prompt as: %s", res.RevisedPrompt)
	}
	if res.Text != "" {
		fmt.Fprintf(&b, "\nThe model said: %s", res.Text)
	}
	blocks := []llm.ContentBlock{llm.TextBlock(b.String())}
	attached := 0
	for _, img := range got {
		if len(img.Data) > ReadMaxImageBytes || img.MediaType == "image/gif" {
			continue
		}
		blocks = append(blocks, llm.ContentBlock{Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: img.MediaType, Data: base64.StdEncoding.EncodeToString(img.Data)}})
		attached++
	}
	if attached < len(got) {
		blocks[0].Text += fmt.Sprintf("\n%d image(s) are too large to show here; Read can open them.", len(got)-attached)
	}
	structured := map[string]any{"type": "image_generate", "model": res.Model, "files": files, "prompt": in.Prompt}
	if res.RevisedPrompt != "" {
		structured["revisedPrompt"] = res.RevisedPrompt
	}
	if res.Priced {
		structured["costUsd"] = res.CostUSD
	}
	summary := "Generated 1 image"
	if len(written) > 1 {
		summary = fmt.Sprintf("Generated %d images", len(written))
	}
	return Result{Content: blocks, Structured: structured, Summary: summary}
}
