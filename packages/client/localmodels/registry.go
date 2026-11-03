package localmodels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// Defaults of Options.
const (
	DefaultProbeTimeout = 800 * time.Millisecond
	DefaultTTL          = time.Minute
)

// ErrDisabledByPolicy is returned while the server's policy disallows local models.
var ErrDisabledByPolicy = &llm.APIError{Type: "permission", Message: "local models are disabled by the server's policy"}

// Provider is a provider with its last probe.
type Provider struct {
	ProviderConfig
	HasAPIKey bool
	Reachable bool
	Version   string
	Error     string
	Models    []Model
	CheckedAt time.Time
}

// Options configure a Registry.
type Options struct {
	// Path of the provider file ("" keeps changes in memory).
	Path    string
	Secrets Secrets
	// HTTP is used for probes and requests (default: a client without overall timeout).
	HTTP *http.Client
	// ProbeTimeout bounds one provider's probe (DefaultProbeTimeout).
	ProbeTimeout time.Duration
	// TTL is how long probes are reused (DefaultTTL).
	TTL time.Duration
	// NoBuiltins leaves out the default endpoints (tests).
	NoBuiltins bool
	Logger     *slog.Logger
}

// Registry holds the local providers of this machine and their probed models.
type Registry struct {
	opts    Options
	http    *http.Client
	prober  *prober
	secrets Secrets
	log     *slog.Logger

	mu        sync.Mutex
	stored    []ProviderConfig // as in the file
	state     map[string]probeResult
	checked   map[string]time.Time
	noTools   map[string]bool // model ids whose server refused tools
	lastProbe time.Time
	probing   chan struct{} // closed when the running full probe ends

	keyMu sync.Mutex
	keys  map[string]string

	policyOff atomic.Bool
	usage     atomic.Pointer[UsageReporter]
	now       func() time.Time
}

// New creates a registry and loads its provider file.
func New(o Options) (*Registry, error) {
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = DefaultProbeTimeout
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.Secrets == nil {
		o.Secrets = &MemorySecrets{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	stored, err := loadStore(o.Path)
	r := &Registry{
		opts: o, http: o.HTTP, secrets: o.Secrets, log: o.Logger,
		prober: &prober{http: o.HTTP, timeout: o.ProbeTimeout},
		stored: stored, state: map[string]probeResult{}, checked: map[string]time.Time{}, noTools: map[string]bool{}, keys: map[string]string{},
		now: time.Now,
	}
	return r, err
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default is the process-wide registry on HARNESS_HOME (local-providers.json, keys in the keychain).
func Default() *Registry {
	defaultOnce.Do(func() {
		r, err := New(Options{
			Path:    filepath.Join(config.Home(), "local-providers.json"),
			Secrets: &KeyringSecrets{FallbackPath: filepath.Join(config.Home(), "local-provider-keys.json"), NoKeyring: os.Getenv("HARNESS_KEYRING") == "0"},
		})
		if err != nil {
			slog.Warn("local providers file unreadable; using defaults", "err", err)
		}
		defaultReg = r
	})
	return defaultReg
}

// Disabled reports HARNESS_NO_LOCAL_MODELS=1 (no detection, no local models).
func Disabled() bool { return os.Getenv("HARNESS_NO_LOCAL_MODELS") == "1" }

// SetPolicyDisabled applies the server's policy (true: local models are not allowed).
func (r *Registry) SetPolicyDisabled(off bool) {
	if r.policyOff.Swap(off) != off {
		r.log.Info("local model policy changed", "disabled", off)
	}
}

// PolicyDisabled reports whether the server's policy disallows local models.
func (r *Registry) PolicyDisabled() bool { return r.policyOff.Load() }

// SetUsageReporter sets where usage of local requests goes.
func (r *Registry) SetUsageReporter(u *UsageReporter) { r.usage.Store(u) }

// configs merges the built-ins with the stored providers (stored built-in entries carry their overrides).
func (r *Registry) configs() []ProviderConfig {
	var out []ProviderConfig
	byID := map[string]ProviderConfig{}
	for _, s := range r.stored {
		byID[s.ID] = s
	}
	if !r.opts.NoBuiltins {
		for _, b := range builtins {
			if s, ok := byID[b.ID]; ok {
				b.Enabled, b.ContextLength = s.Enabled, s.ContextLength
			}
			out = append(out, b)
		}
	}
	for _, s := range r.stored {
		if isBuiltinID(s.ID) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isBuiltinID(id string) bool {
	for _, b := range builtins {
		if b.ID == id {
			return true
		}
	}
	return false
}

func (r *Registry) find(id string) (ProviderConfig, bool) {
	for _, c := range r.configs() {
		if c.ID == id {
			return c, true
		}
	}
	return ProviderConfig{}, false
}

// key returns a provider's API key (cached: the keychain is slow and may prompt).
func (r *Registry) key(id string) string {
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	if k, ok := r.keys[id]; ok {
		return k
	}
	k, err := r.secrets.Get(id)
	if err != nil {
		r.log.Warn("local provider key unreadable", "provider", id, "err", err)
	}
	r.keys[id] = k
	return k
}

func (r *Registry) setKey(id, k string) error {
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	delete(r.keys, id)
	if k == "" {
		return r.secrets.Delete(id)
	}
	return r.secrets.Set(id, k)
}

// List returns the providers; refresh probes now, otherwise probes older than the TTL are refreshed (the
// first probe blocks, later ones run in the background while the cached answer is returned).
func (r *Registry) List(ctx context.Context, refresh bool) []Provider {
	if refresh {
		r.Refresh(ctx, "")
	} else {
		r.ensureFresh(ctx)
	}
	return r.snapshot()
}

func (r *Registry) ensureFresh(ctx context.Context) {
	if r.PolicyDisabled() || Disabled() {
		return
	}
	r.mu.Lock()
	never := r.lastProbe.IsZero()
	stale := r.now().Sub(r.lastProbe) > r.opts.TTL
	r.mu.Unlock()
	switch {
	case never:
		r.Refresh(ctx, "")
	case stale:
		go r.Refresh(context.WithoutCancel(ctx), "")
	}
}

func (r *Registry) snapshot() []Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Provider
	for _, c := range r.configs() {
		p := Provider{ProviderConfig: c}
		if st, ok := r.state[c.ID]; ok && c.Enabled {
			p.Reachable, p.Version, p.Error = st.reachable, st.version, st.err
			p.Models = append([]Model(nil), st.models...)
			for i := range p.Models {
				if r.noTools[p.Models[i].ID] {
					p.Models[i].Tools = boolp(false)
				}
			}
			p.CheckedAt = r.checked[c.ID]
		}
		p.HasAPIKey = r.key(c.ID) != ""
		out = append(out, p)
	}
	return out
}

// Refresh probes all enabled providers (id "") or one, in parallel.
func (r *Registry) Refresh(ctx context.Context, id string) {
	if r.PolicyDisabled() || Disabled() {
		return
	}
	if id == "" {
		r.mu.Lock()
		if ch := r.probing; ch != nil {
			r.mu.Unlock()
			select {
			case <-ch:
			case <-ctx.Done():
			}
			return
		}
		ch := make(chan struct{})
		r.probing = ch
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.probing = nil
			r.lastProbe = r.now()
			r.mu.Unlock()
			close(ch)
		}()
	}
	r.mu.Lock()
	var targets []ProviderConfig
	for _, c := range r.configs() {
		if c.Enabled && (id == "" || c.ID == id) {
			targets = append(targets, c)
		}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	results := make([]probeResult, len(targets))
	for i, c := range targets {
		wg.Add(1)
		go func(i int, c ProviderConfig) {
			defer wg.Done()
			results[i] = r.prober.probe(ctx, c, r.key(c.ID))
		}(i, c)
	}
	wg.Wait()
	r.mu.Lock()
	now := r.now()
	for i, c := range targets {
		r.state[c.ID] = results[i]
		r.checked[c.ID] = now
	}
	r.mu.Unlock()
}

// invalidate drops a provider's probe so the next lookup probes it again.
func (r *Registry) invalidate(id string) {
	r.mu.Lock()
	delete(r.state, id)
	r.mu.Unlock()
	r.Refresh(context.Background(), id)
}

// Models returns the models of reachable enabled providers (for the merged catalog), with provider names.
func (r *Registry) Models(ctx context.Context) []CatalogModel {
	if r.PolicyDisabled() || Disabled() {
		return nil
	}
	var out []CatalogModel
	for _, p := range r.List(ctx, false) {
		if !p.Enabled || !p.Reachable {
			continue
		}
		for _, m := range p.Models {
			out = append(out, CatalogModel{Model: m, ProviderID: p.ID, ProviderName: p.Name})
		}
	}
	return out
}

// CatalogModel is a local model with its provider.
type CatalogModel struct {
	Model
	ProviderID   string
	ProviderName string
}

// Lookup finds a local model id in the cached probes, probing its provider once if it is not known.
func (r *Registry) Lookup(ctx context.Context, id string) (CatalogModel, error) {
	cfg, m, err := r.resolve(ctx, id)
	if err != nil {
		return CatalogModel{}, err
	}
	return CatalogModel{Model: m, ProviderID: cfg.ID, ProviderName: cfg.Name}, nil
}

func (r *Registry) resolve(ctx context.Context, id string) (ProviderConfig, Model, error) {
	if r.PolicyDisabled() {
		return ProviderConfig{}, Model{}, ErrDisabledByPolicy
	}
	if Disabled() {
		return ProviderConfig{}, Model{}, &llm.APIError{Type: "invalid_request", Message: "local models are turned off on this client (HARNESS_NO_LOCAL_MODELS=1)"}
	}
	pid, name, ok := ParseModelID(id)
	if !ok {
		return ProviderConfig{}, Model{}, &llm.APIError{Type: "invalid_request", Message: fmt.Sprintf("malformed local model id %q (want local:<provider>/<model>)", id)}
	}
	r.mu.Lock()
	cfg, found := r.find(pid)
	r.mu.Unlock()
	if !found {
		return cfg, Model{}, &llm.APIError{Type: "invalid_request", Message: fmt.Sprintf("unknown local provider %q on this machine", pid)}
	}
	if !cfg.Enabled {
		return cfg, Model{}, &llm.APIError{Type: "invalid_request", Message: fmt.Sprintf("local provider %s is disabled", cfg.Name)}
	}
	look := func() (Model, bool, bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		st, ok := r.state[pid]
		if !ok {
			return Model{}, false, false
		}
		for _, m := range st.models {
			if m.Name == name {
				if r.noTools[m.ID] {
					m.Tools = boolp(false)
				}
				return m, true, true
			}
		}
		return Model{}, false, true
	}
	m, ok, probed := look()
	if !ok {
		r.Refresh(ctx, pid)
		m, ok, probed = look()
	}
	if !ok {
		r.mu.Lock()
		st := r.state[pid]
		r.mu.Unlock()
		if probed && !st.reachable {
			return cfg, Model{}, &llm.APIError{Type: "upstream", Message: fmt.Sprintf("%s is not reachable at %s: %s", cfg.Name, cfg.BaseURL, st.err)}
		}
		// Unlisted (e.g. an alias the server accepts): try it with unknown capabilities.
		m = Model{ID: id, Name: name, DisplayName: name}
	}
	return cfg, m, nil
}

// AddInput describes a new provider.
type AddInput struct {
	Kind          Kind
	Name          string
	BaseURL       string
	APIKey        string
	ContextLength int
}

// Add stores a new provider and probes it.
func (r *Registry) Add(ctx context.Context, in AddInput) (Provider, error) {
	if in.Kind == "" {
		in.Kind = KindCustom
	}
	if !in.Kind.Valid() {
		return Provider{}, &llm.APIError{Type: "invalid_request", Message: fmt.Sprintf("unknown provider kind %q", in.Kind)}
	}
	base, ok := normalizeBaseURL(in.BaseURL)
	if !ok {
		return Provider{}, &llm.APIError{Type: "invalid_request", Message: "base_url must be an http(s) URL"}
	}
	if in.ContextLength < 0 {
		return Provider{}, &llm.APIError{Type: "invalid_request", Message: "context_length must not be negative"}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = in.Kind.DisplayName()
	}
	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	c := ProviderConfig{ID: string(in.Kind) + "-" + hex.EncodeToString(suffix[:]), Kind: in.Kind, Name: name, BaseURL: base, Enabled: true, ContextLength: in.ContextLength}
	if in.APIKey != "" {
		if err := r.setKey(c.ID, in.APIKey); err != nil {
			return Provider{}, fmt.Errorf("store the API key: %w", err)
		}
	}
	r.mu.Lock()
	r.stored = append(r.stored, c)
	err := saveStore(r.opts.Path, r.stored)
	r.mu.Unlock()
	if err != nil {
		return Provider{}, err
	}
	r.Refresh(ctx, c.ID)
	return r.get(c.ID)
}

// UpdateInput changes a provider; nil fields stay.
type UpdateInput struct {
	Name          *string
	BaseURL       *string
	APIKey        *string
	Enabled       *bool
	ContextLength *int
}

// Update changes a provider. Built-in ones accept Enabled and ContextLength only.
func (r *Registry) Update(ctx context.Context, id string, in UpdateInput) (Provider, error) {
	r.mu.Lock()
	cfg, ok := r.find(id)
	if !ok {
		r.mu.Unlock()
		return Provider{}, &llm.APIError{Status: 404, Type: "not_found", Message: fmt.Sprintf("no local provider %q", id)}
	}
	if cfg.Builtin && (in.Name != nil || in.BaseURL != nil || in.APIKey != nil) {
		r.mu.Unlock()
		return Provider{}, &llm.APIError{Type: "invalid_request", Message: "built-in providers can only be enabled, disabled or given a context length; add a custom provider for another address"}
	}
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n != "" {
			cfg.Name = n
		}
	}
	if in.BaseURL != nil {
		base, ok := normalizeBaseURL(*in.BaseURL)
		if !ok {
			r.mu.Unlock()
			return Provider{}, &llm.APIError{Type: "invalid_request", Message: "base_url must be an http(s) URL"}
		}
		cfg.BaseURL = base
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.ContextLength != nil {
		if *in.ContextLength < 0 {
			r.mu.Unlock()
			return Provider{}, &llm.APIError{Type: "invalid_request", Message: "context_length must not be negative"}
		}
		cfg.ContextLength = *in.ContextLength
	}
	replaced := false
	for i, s := range r.stored {
		if s.ID == id {
			r.stored[i], replaced = cfg, true
		}
	}
	if !replaced {
		r.stored = append(r.stored, cfg)
	}
	delete(r.state, id)
	err := saveStore(r.opts.Path, r.stored)
	r.mu.Unlock()
	if err != nil {
		return Provider{}, err
	}
	if in.APIKey != nil {
		if err = r.setKey(id, *in.APIKey); err != nil {
			return Provider{}, fmt.Errorf("store the API key: %w", err)
		}
	}
	if cfg.Enabled {
		r.Refresh(ctx, id)
	}
	return r.get(id)
}

// Remove deletes a user-added provider and its key.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	cfg, ok := r.find(id)
	if !ok {
		r.mu.Unlock()
		return &llm.APIError{Status: 404, Type: "not_found", Message: fmt.Sprintf("no local provider %q", id)}
	}
	if cfg.Builtin {
		r.mu.Unlock()
		return errBuiltin
	}
	kept := r.stored[:0]
	for _, s := range r.stored {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	r.stored = kept
	delete(r.state, id)
	err := saveStore(r.opts.Path, r.stored)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	return r.setKey(id, "")
}

// errBuiltin refuses removing a built-in provider.
var errBuiltin = errors.New("built-in providers cannot be removed; disable them instead")

// IsBuiltinError reports the error of removing a built-in provider.
func IsBuiltinError(err error) bool { return errors.Is(err, errBuiltin) }

func (r *Registry) get(id string) (Provider, error) {
	for _, p := range r.snapshot() {
		if p.ID == id {
			return p, nil
		}
	}
	return Provider{}, &llm.APIError{Status: 404, Type: "not_found", Message: fmt.Sprintf("no local provider %q", id)}
}

func (r *Registry) ollamaProvider(id string) (ProviderConfig, error) {
	if r.PolicyDisabled() {
		return ProviderConfig{}, ErrDisabledByPolicy
	}
	if id == "" {
		id = "ollama"
	}
	r.mu.Lock()
	c, ok := r.find(id)
	r.mu.Unlock()
	if !ok {
		return c, &llm.APIError{Status: 404, Type: "not_found", Message: fmt.Sprintf("no local provider %q", id)}
	}
	if c.Kind != KindOllama {
		return c, &llm.APIError{Type: "invalid_request", Message: fmt.Sprintf("%s is not an Ollama server", c.Name)}
	}
	return c, nil
}

// Stream sends a request for a local model id.
func (r *Registry) Stream(ctx context.Context, req *llm.Request) (gateway.EventStream, error) {
	cfg, m, err := r.resolve(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	withTools := len(req.Tools) > 0 && (m.Tools == nil || *m.Tools)
	started := r.now()
	st, err := r.open(ctx, cfg, m, req, withTools, started)
	if err != nil && withTools && isNoToolsError(err) {
		// The model has no tool support: remember it and answer without tools.
		r.mu.Lock()
		r.noTools[m.ID] = true
		r.mu.Unlock()
		r.log.Info("local model does not support tools; continuing without them", "model", m.ID)
		st, err = r.open(ctx, cfg, m, req, false, started)
	}
	if err != nil {
		r.record(req, cfg, m, llm.Usage{}, "", err, started)
		return nil, err
	}
	return st, nil
}

func (r *Registry) open(ctx context.Context, cfg ProviderConfig, m Model, req *llm.Request, withTools bool, started time.Time) (gateway.EventStream, error) {
	key := r.key(cfg.ID)
	onDone := func(err error, u llm.Usage, stop string) { r.record(req, cfg, m, u, stop, err, started) }
	if cfg.Kind == KindOllama {
		body, err := postStream(ctx, r.http, rootURL(cfg.BaseURL)+"/api/chat", key, buildOllamaRequest(req, m, withTools), cfg.Name)
		if err != nil {
			return nil, err
		}
		return ollamaStream(body, req.Model, onDone), nil
	}
	body, err := postStream(ctx, r.http, openAIBase(cfg)+"/chat/completions", key, buildOpenAIRequest(req, m, withTools), cfg.Name)
	if err != nil {
		return nil, err
	}
	return openAIStream(body, req.Model, onDone), nil
}

func (r *Registry) record(req *llm.Request, cfg ProviderConfig, m Model, u llm.Usage, stop string, err error, started time.Time) {
	rep := r.usage.Load()
	if rep == nil {
		return
	}
	status := "ok"
	switch {
	case errors.Is(err, errAborted) || errors.Is(err, context.Canceled):
		status = "aborted"
	case err != nil:
		status = "error"
	}
	rec := UsageRecord{
		ModelID: req.Model, ProviderID: cfg.ID, Usage: u, Status: status, StopReason: stop,
		DurationMs: r.now().Sub(started).Milliseconds(), CreatedAt: started.UTC(),
	}
	if md := req.Metadata; md != nil {
		rec.SessionID, rec.RequestClass, rec.AgentID = md.SessionID, md.RequestClass, md.AgentID
	}
	rep.Add(rec)
}
