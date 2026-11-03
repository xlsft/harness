// Package registry reads the public ACP agent registry, caches it in HARNESS_HOME and installs agents from it.
//
// Everything here is local to the machine: the cache (HARNESS_HOME/acp/registry.json), the installed agents
// (HARNESS_HOME/acp/agents/<id>/<version>/) and their state (HARNESS_HOME/acp/state.json: installed version,
// what is known about sign-in) are never synced.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the public registry.
const DefaultURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

// DefaultTTL is how long a fetched registry is used before it is fetched again.
const DefaultTTL = time.Hour

// maxRegistryBytes and maxIconBytes bound what is read from the network.
const (
	maxRegistryBytes = 8 << 20
	maxIconBytes     = 256 << 10
)

// Registry is the registry document.
type Registry struct {
	Version string  `json:"version"`
	Agents  []Agent `json:"agents"`
}

// Agent is one registry entry.
type Agent struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Description  string       `json:"description,omitempty"`
	Repository   string       `json:"repository,omitempty"`
	Website      string       `json:"website,omitempty"`
	Authors      []string     `json:"authors,omitempty"`
	License      string       `json:"license,omitempty"`
	Icon         string       `json:"icon,omitempty"`
	Distribution Distribution `json:"distribution"`
}

// Distribution says how an agent is run: per-platform archives, an npm package (npx) or a Python package (uvx).
type Distribution struct {
	Binary map[string]BinaryTarget `json:"binary,omitempty"`
	Npx    *PackageDist            `json:"npx,omitempty"`
	Uvx    *PackageDist            `json:"uvx,omitempty"`
}

// BinaryTarget is the archive of one platform.
type BinaryTarget struct {
	Archive string            `json:"archive"`
	Cmd     string            `json:"cmd"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Sha256  string            `json:"sha256,omitempty"`
}

// PackageDist is an npx or uvx package.
type PackageDist struct {
	Package string            `json:"package"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Kind is a distribution kind.
type Kind string

const (
	KindBinary Kind = "binary"
	KindNpx    Kind = "npx"
	KindUvx    Kind = "uvx"
)

// Find returns the agent with id.
func (r *Registry) Find(id string) (Agent, bool) {
	if r == nil {
		return Agent{}, false
	}
	for _, a := range r.Agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Parse decodes and checks a registry document.
func Parse(data []byte) (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("acp registry: %w", err)
	}
	out := r.Agents[:0]
	for _, a := range r.Agents {
		if validID(a.ID) && a.Version != "" {
			out = append(out, a)
		}
	}
	r.Agents = out
	sort.SliceStable(r.Agents, func(i, j int) bool { return strings.ToLower(r.Agents[i].Name) < strings.ToLower(r.Agents[j].Name) })
	return &r, nil
}

// validID accepts ids that are safe as a path segment.
func validID(id string) bool {
	if id == "" || len(id) > 100 || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// validVersion accepts versions that are safe as a path segment.
func validVersion(v string) bool { return validID(v) }

// Platform is this machine's registry platform key, e.g. "darwin-aarch64", "linux-x86_64".
func Platform() string { return platformKey(runtime.GOOS, runtime.GOARCH) }

func platformKey(goos, goarch string) string {
	arch := goarch
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return goos + "-" + arch
}

// Manager reads the registry and manages installed agents under Dir.
type Manager struct {
	// Dir is HARNESS_HOME/acp.
	Dir string
	// URL of the registry; DefaultURL when empty.
	URL string
	// TTL of the cache; DefaultTTL when 0.
	TTL time.Duration
	// HTTP client; a client with a timeout when nil.
	HTTP *http.Client
	// LookPath finds runtimes (npx, uvx); exec.LookPath when nil.
	LookPath func(string) (string, error)
	// Platform overrides Platform() (tests).
	Platform string
	// Now overrides time.Now (tests).
	Now func() time.Time

	mu       sync.Mutex
	installs sync.Map // agent id -> *sync.Mutex
}

// New returns a manager for dir with the defaults.
func New(dir string) *Manager { return &Manager{Dir: dir} }

func (m *Manager) url() string {
	if m.URL != "" {
		return m.URL
	}
	return DefaultURL
}

func (m *Manager) ttl() time.Duration {
	if m.TTL > 0 {
		return m.TTL
	}
	return DefaultTTL
}

func (m *Manager) http() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) platform() string {
	if m.Platform != "" {
		return m.Platform
	}
	return Platform()
}

func (m *Manager) lookPath(name string) (string, error) {
	if m.LookPath != nil {
		return m.LookPath(name)
	}
	return exec.LookPath(name)
}

type cacheMeta struct {
	ETag      string    `json:"etag,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
	URL       string    `json:"url,omitempty"`
}

func (m *Manager) cachePath() string { return filepath.Join(m.Dir, "registry.json") }
func (m *Manager) metaPath() string  { return filepath.Join(m.Dir, "registry.meta.json") }
func (m *Manager) iconPath(id string) string {
	return filepath.Join(m.Dir, "icons", id+".svg")
}

// Snapshot is the registry as loaded.
type Snapshot struct {
	Registry *Registry
	// FetchedAt is when the cached copy was fetched; zero without a cache.
	FetchedAt time.Time
	// Err is the fetch error when the cache (or nothing) was used instead.
	Err error
}

// Load returns the registry: the cache while it is younger than the TTL (unless refresh), else a fresh copy
// (conditional on the cached ETag). When the fetch fails the cache is used and Err says why.
func (m *Manager) Load(ctx context.Context, refresh bool) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	cached, meta := m.readCache()
	if cached != nil && !refresh && meta.URL == m.url() && m.now().Sub(meta.FetchedAt) < m.ttl() {
		return Snapshot{Registry: cached, FetchedAt: meta.FetchedAt}
	}
	reg, newMeta, err := m.fetch(ctx, cached, meta)
	if err != nil {
		return Snapshot{Registry: cached, FetchedAt: meta.FetchedAt, Err: err}
	}
	return Snapshot{Registry: reg, FetchedAt: newMeta.FetchedAt}
}

// Cached returns the cached registry without fetching (nil without a cache).
func (m *Manager) Cached() *Registry {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, _ := m.readCache()
	return r
}

func (m *Manager) readCache() (*Registry, cacheMeta) {
	var meta cacheMeta
	if b, err := os.ReadFile(m.metaPath()); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	b, err := os.ReadFile(m.cachePath())
	if err != nil {
		return nil, meta
	}
	r, err := Parse(b)
	if err != nil {
		return nil, meta
	}
	return r, meta
}

func (m *Manager) fetch(ctx context.Context, cached *Registry, meta cacheMeta) (*Registry, cacheMeta, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url(), nil)
	if err != nil {
		return nil, meta, err
	}
	if cached != nil && meta.ETag != "" && meta.URL == m.url() {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.http().Do(req)
	if err != nil {
		return nil, meta, fmt.Errorf("fetch the ACP registry: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && cached != nil:
		meta.FetchedAt = m.now()
		_ = writeJSON(m.metaPath(), meta)
		return cached, meta, nil
	case resp.StatusCode != http.StatusOK:
		return nil, meta, fmt.Errorf("fetch the ACP registry: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBytes+1))
	if err != nil {
		return nil, meta, fmt.Errorf("fetch the ACP registry: %w", err)
	}
	if len(body) > maxRegistryBytes {
		return nil, meta, errors.New("fetch the ACP registry: document too large")
	}
	reg, err := Parse(body)
	if err != nil {
		return nil, meta, err
	}
	if err := writeFileAtomic(m.cachePath(), body, 0o600); err != nil {
		return nil, meta, err
	}
	meta = cacheMeta{ETag: resp.Header.Get("ETag"), FetchedAt: m.now(), URL: m.url()}
	_ = writeJSON(m.metaPath(), meta)
	m.fetchIcons(ctx, reg)
	return reg, meta, nil
}

// fetchIcons caches the SVG icons that are not cached yet (best effort, a few at a time).
func (m *Manager) fetchIcons(ctx context.Context, reg *Registry) {
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, a := range reg.Agents {
		if !strings.HasPrefix(a.Icon, "https://") && !strings.HasPrefix(a.Icon, "http://") {
			continue
		}
		p := m.iconPath(a.ID)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(a Agent, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, a.Icon, nil)
			if err != nil {
				return
			}
			resp, err := m.http().Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
			if err != nil || len(b) > maxIconBytes || !strings.Contains(string(b), "<svg") {
				return
			}
			_ = writeFileAtomic(p, b, 0o600)
		}(a, p)
	}
	wg.Wait()
}

// IconSVG returns the cached SVG of an agent ("" when not cached).
func (m *Manager) IconSVG(id string) string {
	if !validID(id) {
		return ""
	}
	b, err := os.ReadFile(m.iconPath(id))
	if err != nil {
		return ""
	}
	return string(b)
}

// Resolution is how an agent runs on this machine.
type Resolution struct {
	Kind Kind
	// Runtime is "npx" / "uvx" for those kinds.
	Runtime string
	// Available: it can run here; Reason says why not.
	Available bool
	Reason    string
	Binary    *BinaryTarget
	Package   *PackageDist
}

// Resolve picks the distribution for this platform: a binary build first, then npx, then uvx (the first whose
// runtime is on PATH; else the first that exists, unavailable).
func (m *Manager) Resolve(a Agent) Resolution {
	if t, ok := a.Distribution.Binary[m.platform()]; ok && t.Archive != "" && t.Cmd != "" {
		t := t
		return Resolution{Kind: KindBinary, Available: true, Binary: &t}
	}
	var first *Resolution
	for _, c := range []struct {
		kind Kind
		bin  string
		need string
		dist *PackageDist
	}{{KindNpx, "npx", "node (npx)", a.Distribution.Npx}, {KindUvx, "uvx", "uv (uvx)", a.Distribution.Uvx}} {
		if c.dist == nil || c.dist.Package == "" {
			continue
		}
		r := Resolution{Kind: c.kind, Runtime: c.bin, Package: c.dist}
		if _, err := m.lookPath(c.bin); err == nil {
			r.Available = true
			return r
		}
		r.Reason = c.need + " is not on PATH"
		if first == nil {
			first = &r
		}
	}
	if first != nil {
		return *first
	}
	if len(a.Distribution.Binary) > 0 {
		return Resolution{Kind: KindBinary, Reason: "no build for " + m.platform()}
	}
	return Resolution{Reason: "the registry has no distribution this client can run"}
}

// Runtimes lists the runtimes found on PATH ("npx", "uvx").
func (m *Manager) Runtimes() []string {
	var out []string
	for _, r := range []string{"npx", "uvx"} {
		if _, err := m.lookPath(r); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
