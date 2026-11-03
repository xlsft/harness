package localmodels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Model is a model a local provider serves.
type Model struct {
	ID          string
	Name        string
	DisplayName string
	// ContextWindow is what the client uses (for Ollama the num_ctx it requests); 0 when unknown.
	ContextWindow int
	// MaxContextWindow is the model's own limit when reported; 0 when unknown.
	MaxContextWindow int
	// Capabilities; nil means unknown.
	Tools, Vision, Thinking *bool
	SizeBytes               int64
	Family                  string
	ParameterSize           string
	Quantization            string
	ModifiedAt              string
}

// probeResult is the outcome of probing one provider.
type probeResult struct {
	reachable bool
	version   string
	err       string
	models    []Model
}

func boolp(b bool) *bool { return &b }

// prober talks to providers for detection and model listing.
type prober struct {
	http    *http.Client
	timeout time.Duration
	// showCache keeps Ollama /api/show answers by base URL, name and modification time.
	mu        sync.Mutex
	showCache map[string]ollamaShow
}

func (p *prober) probe(ctx context.Context, c ProviderConfig, key string) probeResult {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	var (
		r   probeResult
		err error
	)
	switch c.Kind {
	case KindOllama:
		r, err = p.probeOllama(ctx, c)
	default:
		r, err = p.probeOpenAI(ctx, c, key)
	}
	if err != nil {
		r.reachable = false
		r.err = describeProbeError(err)
		r.models = nil
		return r
	}
	r.reachable = true
	for i := range r.models {
		r.models[i].ID = ModelID(c.ID, r.models[i].Name)
		if r.models[i].DisplayName == "" {
			r.models[i].DisplayName = r.models[i].Name
		}
	}
	sort.SliceStable(r.models, func(i, j int) bool { return r.models[i].Name < r.models[j].Name })
	return r
}

func describeProbeError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "not running (no answer in time)"
	case strings.Contains(err.Error(), "connection refused"):
		return "not running (connection refused)"
	}
	return err.Error()
}

// getJSON GETs url into v; a non-2xx status is an error.
func (p *prober) getJSON(ctx context.Context, url, key string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, errorMessage(body))
	}
	if v == nil {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: %w", url, err)
	}
	return resp.StatusCode, nil
}

func (p *prober) postJSON(ctx context.Context, url string, in, out any) error {
	data, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("POST %s: HTTP %d: %s", url, resp.StatusCode, errorMessage(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// errorMessage extracts {"error":"..."} or {"error":{"message":"..."}} from an error body.
func errorMessage(body []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil {
		if len(e.Error) > 0 {
			var s string
			if json.Unmarshal(e.Error, &s) == nil && s != "" {
				return s
			}
			var o struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Error, &o) == nil && o.Message != "" {
				return o.Message
			}
		}
		if e.Message != "" {
			return e.Message
		}
		if e.Detail != "" {
			return e.Detail
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// ---- Ollama ----

type ollamaTags struct {
	Models []struct {
		Name       string `json:"name"`
		Model      string `json:"model"`
		ModifiedAt string `json:"modified_at"`
		Size       int64  `json:"size"`
		Details    struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

type ollamaShow struct {
	ModelInfo    map[string]json.RawMessage `json:"model_info"`
	Capabilities []string                   `json:"capabilities"`
}

// contextLength reads "<arch>.context_length" from model_info.
func (s ollamaShow) contextLength() int {
	var arch string
	_ = json.Unmarshal(s.ModelInfo["general.architecture"], &arch)
	keys := []string{arch + ".context_length"}
	for k := range s.ModelInfo {
		if strings.HasSuffix(k, ".context_length") {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		var n float64
		if raw, ok := s.ModelInfo[k]; ok && json.Unmarshal(raw, &n) == nil && n > 0 {
			return int(n)
		}
	}
	return 0
}

func (p *prober) probeOllama(ctx context.Context, c ProviderConfig) (probeResult, error) {
	root := rootURL(c.BaseURL)
	var r probeResult
	var ver struct {
		Version string `json:"version"`
	}
	if _, err := p.getJSON(ctx, root+"/api/version", "", &ver); err != nil {
		return r, err
	}
	r.version = ver.Version
	var tags ollamaTags
	if _, err := p.getJSON(ctx, root+"/api/tags", "", &tags); err != nil {
		return r, err
	}
	want := c.ContextLength
	if want <= 0 {
		want = DefaultContextLength
	}
	models := make([]Model, len(tags.Models))
	keep := make([]bool, len(tags.Models))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, t := range tags.Models {
		name := t.Name
		if name == "" {
			name = t.Model
		}
		m := Model{Name: name, SizeBytes: t.Size, ModifiedAt: t.ModifiedAt, Family: t.Details.Family, ParameterSize: t.Details.ParameterSize, Quantization: t.Details.QuantizationLevel}
		keep[i] = true
		wg.Add(1)
		go func(i int, m Model) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			show, ok := p.ollamaShow(ctx, root, m.Name, m.ModifiedAt)
			if ok {
				m.MaxContextWindow = show.contextLength()
				if len(show.Capabilities) > 0 {
					caps := map[string]bool{}
					for _, c := range show.Capabilities {
						caps[c] = true
					}
					if !caps["completion"] {
						keep[i] = false // embedding-only models cannot chat
					}
					m.Tools, m.Vision, m.Thinking = boolp(caps["tools"]), boolp(caps["vision"]), boolp(caps["thinking"])
				}
			}
			m.ContextWindow = want
			if m.MaxContextWindow > 0 && m.MaxContextWindow < want {
				m.ContextWindow = m.MaxContextWindow
			}
			models[i] = m
		}(i, m)
	}
	wg.Wait()
	for i, m := range models {
		if keep[i] {
			r.models = append(r.models, m)
		}
	}
	return r, nil
}

func (p *prober) ollamaShow(ctx context.Context, root, name, modified string) (ollamaShow, bool) {
	key := root + "\x00" + name + "\x00" + modified
	p.mu.Lock()
	s, ok := p.showCache[key]
	p.mu.Unlock()
	if ok {
		return s, true
	}
	if err := p.postJSON(ctx, root+"/api/show", map[string]string{"model": name}, &s); err != nil {
		return s, false
	}
	p.mu.Lock()
	if p.showCache == nil {
		p.showCache = map[string]ollamaShow{}
	}
	p.showCache[key] = s
	p.mu.Unlock()
	return s, true
}

// ---- OpenAI-compatible ----

type openAIModels struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
		// vLLM
		MaxModelLen int `json:"max_model_len"`
		// some servers
		ContextLength int `json:"context_length"`
		// llama.cpp
		Meta *struct {
			NCtxTrain int `json:"n_ctx_train"`
		} `json:"meta"`
	} `json:"data"`
}

// lmStudioModels is LM Studio's richer REST API (/api/v0/models).
type lmStudioModels struct {
	Data []struct {
		ID                  string   `json:"id"`
		Type                string   `json:"type"` // llm | vlm | embeddings
		MaxContextLength    int      `json:"max_context_length"`
		LoadedContextLength int      `json:"loaded_context_length"`
		Capabilities        []string `json:"capabilities"`
		Quantization        string   `json:"quantization"`
		Arch                string   `json:"arch"`
	} `json:"data"`
}

func (p *prober) probeOpenAI(ctx context.Context, c ProviderConfig, key string) (probeResult, error) {
	var r probeResult
	root := rootURL(c.BaseURL)
	// llama.cpp and MLX share port 8080: llama-server answers /props, mlx_lm.server does not.
	switch c.Kind {
	case KindLlamaCpp:
		var props struct {
			BuildInfo string `json:"build_info"`
		}
		if _, err := p.getJSON(ctx, root+"/props", key, &props); err != nil {
			return r, err
		}
		r.version = props.BuildInfo
	case KindMLX:
		if _, err := p.getJSON(ctx, root+"/props", key, nil); err == nil {
			return r, errors.New("the port is served by llama.cpp, not mlx_lm.server")
		}
	case KindVLLM:
		var v struct {
			Version string `json:"version"`
		}
		if _, err := p.getJSON(ctx, root+"/version", key, &v); err == nil {
			r.version = v.Version
		}
	}
	var list openAIModels
	if _, err := p.getJSON(ctx, openAIBase(c)+"/models", key, &list); err != nil {
		return r, err
	}
	for _, d := range list.Data {
		m := Model{Name: d.ID}
		switch {
		case d.MaxModelLen > 0:
			m.MaxContextWindow = d.MaxModelLen
		case d.ContextLength > 0:
			m.MaxContextWindow = d.ContextLength
		case d.Meta != nil && d.Meta.NCtxTrain > 0:
			m.MaxContextWindow = d.Meta.NCtxTrain
		}
		m.ContextWindow = m.MaxContextWindow
		r.models = append(r.models, m)
	}
	if c.Kind == KindLMStudio {
		var rich lmStudioModels
		if _, err := p.getJSON(ctx, root+"/api/v0/models", key, &rich); err == nil {
			byID := map[string]int{}
			for i, m := range r.models {
				byID[m.Name] = i
			}
			var out []Model
			for _, d := range rich.Data {
				i, ok := byID[d.ID]
				if !ok {
					continue
				}
				if d.Type == "embeddings" {
					byID[d.ID] = -1
					continue
				}
				m := &r.models[i]
				m.MaxContextWindow = d.MaxContextLength
				m.ContextWindow = d.MaxContextLength
				if d.LoadedContextLength > 0 {
					m.ContextWindow = d.LoadedContextLength
				}
				m.Vision = boolp(d.Type == "vlm")
				tools := false
				for _, c := range d.Capabilities {
					if c == "tool_use" {
						tools = true
					}
				}
				if len(d.Capabilities) > 0 {
					m.Tools = boolp(tools)
				}
				m.Quantization, m.Family = d.Quantization, d.Arch
			}
			for _, m := range r.models {
				if byID[m.Name] >= 0 {
					out = append(out, m)
				}
			}
			r.models = out
		}
	}
	return r, nil
}
