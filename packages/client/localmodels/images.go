package localmodels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/xlsft/harness/packages/client/images"
	"github.com/xlsft/harness/packages/client/llm"
)

// Device-local image models (docs/design/images.md): an OpenAI-compatible local server that serves /v1/images/generations
// and /v1/images/edits (LocalAI, or any of the OpenAI-compatible kinds here). The id is "local:<provider>/<model>" like a
// local chat model, but local image models are not listed by a probe (a /v1/models list does not say what generates
// images): the user names the model on the device. Nothing about them reaches the server; they cost nothing.

// maxLocalImageBytes bounds one answer of a local image server.
const maxLocalImageBytes = 200 << 20

// ImageProvider returns the local provider of an image model id, refusing what cannot serve images.
func (r *Registry) ImageProvider(id string) (ProviderConfig, string, error) {
	if r.PolicyDisabled() {
		return ProviderConfig{}, "", ErrDisabledByPolicy
	}
	pid, name, ok := ParseModelID(id)
	if !ok {
		return ProviderConfig{}, "", fmt.Errorf("%q is not a local model id (local:<provider>/<model>)", id)
	}
	cfg, ok := r.find(pid)
	if !ok {
		return ProviderConfig{}, "", fmt.Errorf("there is no local provider %q on this device", pid)
	}
	if !cfg.Enabled {
		return ProviderConfig{}, "", fmt.Errorf("the local provider %s is turned off", cfg.displayName())
	}
	if cfg.Kind == KindOllama {
		return ProviderConfig{}, "", fmt.Errorf("Ollama has no image generation API; use an OpenAI-compatible local server")
	}
	return cfg, name, nil
}

func (c ProviderConfig) displayName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Kind.DisplayName()
}

// GenerateImages implements images.Generator for device-local image models.
func (r *Registry) GenerateImages(ctx context.Context, req images.Request) (*images.Result, error) {
	cfg, name, err := r.ImageProvider(req.Model)
	if err != nil {
		var ae *llm.APIError
		if e, ok := err.(*llm.APIError); ok {
			ae = e
		} else {
			ae = &llm.APIError{Type: "invalid_request", Message: err.Error()}
		}
		return nil, ae
	}
	n := req.Count
	if n <= 0 {
		n = 1
	}
	base := openAIBase(cfg)
	var body io.Reader
	contentType := "application/json"
	url := base + "/images/generations"
	if len(req.Inputs) > 0 {
		url = base + "/images/edits"
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("model", name)
		_ = w.WriteField("prompt", req.Prompt)
		_ = w.WriteField("n", fmt.Sprint(n))
		_ = w.WriteField("response_format", "b64_json")
		if req.Size != "" {
			_ = w.WriteField("size", req.Size)
		}
		for i, in := range req.Inputs {
			h := make(textproto.MIMEHeader)
			fn := in.Name
			if fn == "" {
				fn = fmt.Sprintf("image-%d%s", i+1, images.Extension(in.MediaType))
			}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image[]"; filename=%q`, fn))
			h.Set("Content-Type", in.MediaType)
			part, err := w.CreatePart(h)
			if err != nil {
				return nil, err
			}
			_, _ = part.Write(in.Data)
		}
		_ = w.Close()
		body, contentType = &buf, w.FormDataContentType()
	} else {
		m := map[string]any{"model": name, "prompt": req.Prompt, "n": n, "response_format": "b64_json"}
		if req.Size != "" {
			m["size"] = req.Size
		}
		if req.Quality != "" {
			m["quality"] = req.Quality
		}
		data, _ := json.Marshal(m)
		body = bytes.NewReader(data)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", contentType)
	if k := r.key(cfg.ID); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := r.http.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.APIError{Type: "upstream", Message: fmt.Sprintf("%s is not reachable: %v", cfg.displayName(), err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLocalImageBytes))
	if err != nil {
		return nil, &llm.APIError{Type: "upstream", Message: fmt.Sprintf("%s: %v", cfg.displayName(), err)}
	}
	if resp.StatusCode/100 != 2 {
		return nil, httpError(resp.StatusCode, cfg.displayName(), errorMessage(raw))
	}
	var out struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &llm.APIError{Type: "upstream", Message: cfg.displayName() + " returned a response that is not JSON"}
	}
	res := &images.Result{Model: req.Model, Priced: true}
	for _, d := range out.Data {
		b64 := d.B64JSON
		if b64 == "" && strings.HasPrefix(d.URL, "data:") {
			if i := strings.Index(d.URL, ";base64,"); i > 0 {
				b64 = d.URL[i+len(";base64,"):]
			}
		}
		if b64 == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, &llm.APIError{Type: "upstream", Message: cfg.displayName() + " returned an image that is not valid base64"}
		}
		mt := images.Sniff(data)
		if mt == "" {
			return nil, &llm.APIError{Type: "upstream", Message: cfg.displayName() + " returned something that is not a PNG, JPEG, WebP or GIF image"}
		}
		res.Images = append(res.Images, images.Image{Data: data, MediaType: mt})
		if res.RevisedPrompt == "" {
			res.RevisedPrompt = d.RevisedPrompt
		}
	}
	if len(res.Images) == 0 {
		return nil, &llm.APIError{Type: "upstream", Message: cfg.displayName() + " returned no image"}
	}
	return res, nil
}

var _ images.Generator = (*Router)(nil)

// GenerateImages implements images.Generator: device-local image models on this machine, the others on the server.
func (r *Router) GenerateImages(ctx context.Context, req images.Request) (*images.Result, error) {
	if IsLocal(req.Model) {
		if r.Local == nil {
			return nil, &llm.APIError{Type: "invalid_request", Message: "local models are turned off on this client"}
		}
		return r.Local.GenerateImages(ctx, req)
	}
	g, ok := r.Core.(images.Generator)
	if !ok {
		return nil, &llm.APIError{Type: "invalid_request", Message: "this client's server connection cannot generate images"}
	}
	return g.GenerateImages(ctx, req)
}
