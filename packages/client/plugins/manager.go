package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// LocalMarketplace is the marketplace part of the id of a plugin installed directly from a path or a repository.
const LocalMarketplace = "local"

// State of a plugin here and now.
type Status string

// Statuses, in the order they are looked at.
const (
	StatusActive          Status = "active"
	StatusNeedsApproval   Status = "needs_approval"
	StatusDisabled        Status = "disabled"
	StatusUntrustedFolder Status = "untrusted_folder"
	StatusBlockedByPolicy Status = "blocked_by_policy"
	StatusChangedOnDisk   Status = "changed_on_disk"
	StatusBroken          Status = "broken"
	// StatusNotInstalled is what an admin distributes or a project asks for, not here yet.
	StatusNotInstalled Status = "not_installed"
)

// Record is an installed plugin in the state file.
type Record struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Marketplace string `json:"marketplace"`
	Version     string `json:"version"`
	// Commit and Hash are what is pinned; Dir the folder holding it, relative to the store.
	Commit string `json:"commit,omitempty"`
	Hash   string `json:"hash"`
	Dir    string `json:"dir"`
	// Source is where it was fetched from, for a plugin without a marketplace.
	Source Source `json:"source,omitempty"`
	// Overlay is the marketplace entry it was installed with.
	Overlay *Entry `json:"overlay,omitempty"`
	Enabled bool   `json:"enabled"`
	// ApprovedHash is the content this device approved; it runs only while that is Hash.
	ApprovedHash string `json:"approvedHash,omitempty"`
	ApprovedAt   string `json:"approvedAt,omitempty"`
	// Pinned keeps Update from moving it.
	Pinned      bool   `json:"pinned,omitempty"`
	InstalledAt string `json:"installedAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// MarketplaceRecord is a registered marketplace.
type MarketplaceRecord struct {
	Name   string `json:"name"`
	Source Source `json:"source"`
	// Dir is the copy of it in the store, relative to the store ("" for a local one, read where it is).
	Dir       string `json:"dir,omitempty"`
	Commit    string `json:"commit,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

type stateFile struct {
	Marketplaces map[string]*MarketplaceRecord `json:"marketplaces"`
	Plugins      map[string]*Record            `json:"plugins"`
}

// Manager is the store of this device: HARNESS_HOME/plugins and its state file. It is the same object for one folder in a
// process.
type Manager struct {
	// Root is the folder of the store.
	Root string
	// HTTP fetches marketplace files; nil: the default client.
	HTTP *http.Client
	// PolicyOf overrides the policy (tests); nil: CurrentPolicy.
	PolicyOf func() Policy

	mu   sync.Mutex
	memo map[string]memoEntry
	// syncMu lets one distribution run at a time.
	syncMu sync.Mutex
}

// memoEntry keeps what reading a folder found while its stat stamp stays.
type memoEntry struct {
	stamp, hash string
	plugin      *Plugin
	err         error
}

var (
	managersMu sync.Mutex
	managers   = map[string]*Manager{}
)

// At returns the store at root.
func At(root string) *Manager {
	managersMu.Lock()
	defer managersMu.Unlock()
	m := managers[root]
	if m == nil {
		m = &Manager{Root: root}
		managers[root] = m
	}
	return m
}

// Policy returns the policy in force.
func (m *Manager) Policy() Policy {
	if m.PolicyOf != nil {
		return m.PolicyOf()
	}
	return CurrentPolicy(m.Root)
}

func (m *Manager) statePath() string { return filepath.Join(m.Root, "state.json") }

func (m *Manager) load() (*stateFile, error) {
	st := &stateFile{Marketplaces: map[string]*MarketplaceRecord{}, Plugins: map[string]*Record{}}
	data, err := os.ReadFile(m.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s: %w", m.statePath(), err)
	}
	if st.Marketplaces == nil {
		st.Marketplaces = map[string]*MarketplaceRecord{}
	}
	if st.Plugins == nil {
		st.Plugins = map[string]*Record{}
	}
	return st, nil
}

func (m *Manager) save(st *stateFile) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(m.statePath(), data)
}

// update runs f on the state under the lock and saves it if f succeeds.
func (m *Manager) update(f func(*stateFile) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.load()
	if err != nil {
		return err
	}
	if err := f(st); err != nil {
		return err
	}
	return m.save(st)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (m *Manager) dataDir(id string) string {
	return filepath.Join(m.Root, "data", regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(id, "-"))
}

// ---- marketplaces ----

// Marketplace is a registered marketplace, read.
type Marketplace struct {
	Name, Description string
	Source            Source
	Commit, UpdatedAt string
	// Managed: the organization distributes it, so it can't be removed here.
	Managed bool
	// Refusal is the policy's word on it; Problem says why it can't be read.
	Refusal, Problem string
	Plugins          []Entry
}

// AddMarketplace fetches a marketplace and registers it. Nothing in it runs.
func (m *Manager) AddMarketplace(ctx context.Context, s Source) (*MarketplaceRecord, error) {
	if why := m.Policy().SourceRefusal(s); why != "" {
		return nil, errors.New(why)
	}
	rec, manifest, err := m.fetchMarketplace(ctx, s)
	if err != nil {
		return nil, err
	}
	if reservedName(manifest.Name, s) {
		_ = os.RemoveAll(filepath.Join(m.Root, rec.Dir))
		return nil, fmt.Errorf("the marketplace name %q is reserved", manifest.Name)
	}
	err = m.update(func(st *stateFile) error {
		if cur := st.Marketplaces[rec.Name]; cur != nil && canonicalSource(cur.Source) != canonicalSource(s) {
			return fmt.Errorf("a marketplace named %s is already registered from %s", rec.Name, cur.Source)
		}
		st.Marketplaces[rec.Name] = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

func canonicalSource(s Source) string {
	if s.Source == SourceFile || s.Source == SourceDirectory {
		return s.Path
	}
	return canonicalURL(s.cloneURL())
}

// fetchMarketplace reads a marketplace into the store. Its folder is swapped in whole, so a failure leaves the old one.
func (m *Manager) fetchMarketplace(ctx context.Context, s Source) (*MarketplaceRecord, *MarketplaceManifest, error) {
	dir := filepath.Join(m.Root, "marketplaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	rec := &MarketplaceRecord{Source: s, UpdatedAt: now()}
	switch s.Source {
	case SourceFile, SourceDirectory:
		manifest, _, err := readLocalMarketplace(s)
		if err != nil {
			return nil, nil, err
		}
		rec.Name = manifest.Name
		return rec, manifest, nil
	case SourceURL:
		data, err := fetchFile(ctx, m.HTTP, s)
		if err != nil {
			return nil, nil, err
		}
		manifest, err := parseMarketplace(data)
		if err != nil {
			return nil, nil, err
		}
		rec.Name, rec.Dir = manifest.Name, filepath.Join("marketplaces", manifest.Name)
		dest := filepath.Join(m.Root, rec.Dir)
		if err := writeFileAtomic(filepath.Join(dest, ".url-marketplace", MarketplaceFile), data); err != nil {
			return nil, nil, err
		}
		return rec, manifest, nil
	case SourceGitHub, SourceGit:
	default:
		return nil, nil, fmt.Errorf("this client does not fetch marketplaces from a source of type %q", s.Source)
	}
	tmp, err := os.MkdirTemp(dir, ".tmp-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	checkout := filepath.Join(tmp, "repo")
	commit, err := clone(ctx, s, checkout)
	if err != nil {
		return nil, nil, err
	}
	p, err := findManifest(checkout, s.Path)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := parseMarketplace(data)
	if err != nil {
		return nil, nil, err
	}
	rec.Name, rec.Commit, rec.Dir = manifest.Name, commit, filepath.Join("marketplaces", manifest.Name)
	dest := filepath.Join(m.Root, rec.Dir)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.RemoveAll(dest); err != nil {
		return nil, nil, err
	}
	if err := os.Rename(checkout, dest); err != nil {
		return nil, nil, err
	}
	return rec, manifest, nil
}

func readLocalMarketplace(s Source) (*MarketplaceManifest, string, error) {
	path, root := s.Path, ""
	if s.Source == SourceFile {
		root = filepath.Dir(filepath.Dir(path))
	} else {
		root = s.Path
		p, err := findManifest(root, "")
		if err != nil {
			return nil, "", err
		}
		path = p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	manifest, err := parseMarketplace(data)
	return manifest, root, err
}

// readMarketplace reads a registered marketplace; root is its folder ("" for a file from a URL, with no plugin files).
func (m *Manager) readMarketplace(rec *MarketplaceRecord) (*MarketplaceManifest, string, error) {
	switch rec.Source.Source {
	case SourceFile, SourceDirectory:
		return readLocalMarketplace(rec.Source)
	case SourceURL:
		data, err := os.ReadFile(filepath.Join(m.Root, rec.Dir, ".url-marketplace", MarketplaceFile))
		if err != nil {
			return nil, "", err
		}
		manifest, err := parseMarketplace(data)
		return manifest, "", err
	}
	root := filepath.Join(m.Root, rec.Dir)
	p, err := findManifest(root, rec.Source.Path)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	manifest, err := parseMarketplace(data)
	return manifest, root, err
}

// UpdateMarketplace fetches a marketplace's newest catalog. Installed plugins stay as they are.
func (m *Manager) UpdateMarketplace(ctx context.Context, name string) error {
	st, err := m.snapshot()
	if err != nil {
		return err
	}
	rec := st.Marketplaces[name]
	if rec == nil {
		return fmt.Errorf("no marketplace named %s", name)
	}
	if why := m.Policy().SourceRefusal(rec.Source); why != "" {
		return errors.New(why)
	}
	fresh, _, err := m.fetchMarketplace(ctx, rec.Source)
	if err != nil {
		return err
	}
	if fresh.Name != name {
		return fmt.Errorf("the marketplace now calls itself %s; remove %s and add it again", fresh.Name, name)
	}
	return m.update(func(st *stateFile) error {
		st.Marketplaces[name] = fresh
		return nil
	})
}

// RemoveMarketplace unregisters a marketplace and removes the plugins installed from it.
func (m *Manager) RemoveMarketplace(name string) error {
	if _, ok := m.Policy().Distributed[name]; ok {
		return fmt.Errorf("your organization distributes the marketplace %s", name)
	}
	var removed []*Record
	var dir string
	err := m.update(func(st *stateFile) error {
		rec := st.Marketplaces[name]
		if rec == nil {
			return fmt.Errorf("no marketplace named %s", name)
		}
		dir = rec.Dir
		delete(st.Marketplaces, name)
		for id, r := range st.Plugins {
			if r.Marketplace == name {
				removed = append(removed, r)
				delete(st.Plugins, id)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range removed {
		_ = os.RemoveAll(filepath.Dir(filepath.Join(m.Root, r.Dir)))
		_ = os.RemoveAll(m.dataDir(r.ID))
	}
	if dir != "" {
		_ = os.RemoveAll(filepath.Join(m.Root, dir))
	}
	return nil
}

func (m *Manager) snapshot() (*stateFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load()
}

// Marketplaces lists what is registered, with each catalog as it was last fetched.
func (m *Manager) Marketplaces() ([]Marketplace, error) {
	st, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	pol := m.Policy()
	out := make([]Marketplace, 0, len(st.Marketplaces))
	for _, rec := range st.Marketplaces {
		mk := Marketplace{Name: rec.Name, Source: rec.Source, Commit: rec.Commit, UpdatedAt: rec.UpdatedAt, Refusal: pol.SourceRefusal(rec.Source)}
		_, mk.Managed = pol.Distributed[rec.Name]
		if man, _, err := m.readMarketplace(rec); err != nil {
			mk.Problem = err.Error()
		} else {
			mk.Description, mk.Plugins = man.Description, man.Plugins
		}
		out = append(out, mk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---- install ----

// fetched is a plugin folder in a temporary place.
type fetched struct {
	dir, commit string
	cleanup     func()
}

// fetchEntry brings a marketplace entry's folder to a temporary place.
func (m *Manager) fetchEntry(ctx context.Context, rel string, s Source, marketRoot string) (*fetched, error) {
	tmpRoot := filepath.Join(m.Root, "cache")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(tmpRoot, ".tmp-")
	if err != nil {
		return nil, err
	}
	out := &fetched{cleanup: func() { os.RemoveAll(tmp) }}
	fail := func(err error) (*fetched, error) {
		out.cleanup()
		return nil, err
	}
	dest := filepath.Join(tmp, "plugin")
	switch {
	case s.Source == "":
		if marketRoot == "" {
			return fail(errors.New("a marketplace added by the URL of its file has no plugin folders: its entries need a repository as their source"))
		}
		src := filepath.Join(marketRoot, filepath.FromSlash(rel))
		real, err := filepath.EvalSymlinks(src)
		if err != nil {
			return fail(fmt.Errorf("source path does not exist: %s", src))
		}
		base, err := filepath.EvalSymlinks(marketRoot)
		if err != nil || !inside(real, base) {
			return fail(fmt.Errorf("the source %s leaves the marketplace", rel))
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return fail(err)
		}
		if err := copyTree(real, dest); err != nil {
			return fail(err)
		}
	case s.Source == SourceDirectory:
		if !isDir(s.Path) {
			return fail(fmt.Errorf("source path does not exist: %s", s.Path))
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return fail(err)
		}
		if err := copyTree(s.Path, dest); err != nil {
			return fail(err)
		}
	default:
		checkout := filepath.Join(tmp, "repo")
		commit, err := clone(ctx, s, checkout)
		if err != nil {
			return fail(err)
		}
		out.commit = commit
		src := checkout
		if s.Source == SourceSubdir {
			src = filepath.Join(checkout, filepath.FromSlash(s.Path))
			if !isDir(src) {
				return fail(fmt.Errorf("%s is not a folder of the repository", s.Path))
			}
		}
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return fail(err)
		}
		if err := copyTree(src, dest); err != nil {
			return fail(err)
		}
	}
	out.dir = dest
	return out, nil
}

// resolved is what an install spec points at.
type resolved struct {
	name, marketplace string
	rel               string
	source            Source
	marketRoot        string
	entry             *Entry
	manifest          *MarketplaceManifest
}

var idRe = regexp.MustCompile(`^([^@/\s]+)@([^@/\s]+)$`)

func (m *Manager) resolve(spec string) (*resolved, error) {
	spec = strings.TrimSpace(spec)
	st, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	if match := idRe.FindStringSubmatch(spec); match != nil {
		return m.resolveEntry(st, match[1], match[2])
	}
	if ValidName(spec) && !strings.Contains(spec, "/") {
		var found []string
		names := make([]string, 0, len(st.Marketplaces))
		for n := range st.Marketplaces {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if man, _, err := m.readMarketplace(st.Marketplaces[n]); err == nil && slices.ContainsFunc(man.Plugins, func(e Entry) bool { return e.Name == spec }) {
				found = append(found, n)
			}
		}
		switch len(found) {
		case 0:
			return nil, fmt.Errorf("no marketplace has a plugin named %s", spec)
		case 1:
			return m.resolveEntry(st, spec, found[0])
		}
		return nil, fmt.Errorf("several marketplaces have a plugin named %s: %s", spec, strings.Join(found, ", "))
	}
	s, err := ParseSource(spec)
	if err != nil {
		return nil, err
	}
	if s.Source != SourceDirectory && s.Source != SourceGitHub && s.Source != SourceGit {
		return nil, fmt.Errorf("%s isn't a plugin: install from a folder or a repository", spec)
	}
	if why := m.Policy().SourceRefusal(s); why != "" {
		return nil, errors.New(why)
	}
	return &resolved{marketplace: LocalMarketplace, source: s}, nil
}

func (m *Manager) resolveEntry(st *stateFile, name, market string) (*resolved, error) {
	if market == LocalMarketplace {
		rec := st.Plugins[name+"@"+market]
		if rec == nil {
			return nil, fmt.Errorf("%s@%s is not installed; install it from its folder or repository", name, market)
		}
		return &resolved{name: name, marketplace: market, source: rec.Source}, nil
	}
	rec := st.Marketplaces[market]
	if rec == nil {
		return nil, fmt.Errorf("no marketplace named %s; add it first", market)
	}
	if why := m.Policy().SourceRefusal(rec.Source); why != "" {
		return nil, errors.New(why)
	}
	man, root, err := m.readMarketplace(rec)
	if err != nil {
		return nil, err
	}
	for i := range man.Plugins {
		e := &man.Plugins[i]
		if e.Name != name {
			continue
		}
		if !ValidName(e.Name) {
			return nil, fmt.Errorf("the plugin name %q can't be used", e.Name)
		}
		rel, s, err := entrySource(*e, man)
		if err != nil {
			return nil, err
		}
		return &resolved{name: name, marketplace: market, rel: rel, source: s, marketRoot: root, entry: e, manifest: man}, nil
	}
	return nil, fmt.Errorf("the marketplace %s has no plugin named %s", market, name)
}

func overlayOf(e *Entry) *Overlay {
	if e == nil {
		return nil
	}
	return &Overlay{Strict: e.Strict, Manifest: e.Manifest}
}

// Install fetches a plugin into the store. spec is name@marketplace, a name one marketplace has, a folder or a repository. Nothing
// of it runs until it is approved.
func (m *Manager) Install(ctx context.Context, spec string) (*Record, error) {
	r, err := m.resolve(spec)
	if err != nil {
		return nil, err
	}
	return m.installResolved(ctx, r, nil)
}

func (m *Manager) installResolved(ctx context.Context, r *resolved, prev *Record) (*Record, error) {
	id := r.name + "@" + r.marketplace
	if r.name != "" && m.Policy().PluginBlocked(id) {
		return nil, fmt.Errorf("your organization's policy blocks %s", id)
	}
	f, err := m.fetchEntry(ctx, r.rel, r.source, r.marketRoot)
	if err != nil {
		return nil, err
	}
	defer f.cleanup()
	if r.name == "" {
		man, _, err := ReadManifest(f.dir)
		if err != nil {
			return nil, err
		}
		r.name = man.Name
		if r.name == "" {
			r.name = strings.TrimSuffix(filepath.Base(strings.TrimRight(r.source.String(), "/")), ".git")
		}
		id = r.name + "@" + r.marketplace
		if m.Policy().PluginBlocked(id) {
			return nil, fmt.Errorf("your organization's policy blocks %s", id)
		}
	}
	if !ValidName(r.name) {
		return nil, fmt.Errorf("the plugin name %q can't be used", r.name)
	}
	p, err := Load(f.dir, r.name, r.marketplace, m.dataDir(id), overlayOf(r.entry))
	if err != nil {
		return nil, err
	}
	if p.Contents.Empty() {
		return nil, errors.New("the folder holds no plugin: no manifest components and none of the standard folders")
	}
	hash, err := HashTree(f.dir)
	if err != nil {
		return nil, err
	}
	version := p.Manifest.Version
	if version == "" && r.entry != nil {
		version = r.entry.Version
	}
	if prev != nil && prev.Hash == hash {
		if err := m.update(func(st *stateFile) error {
			if cur := st.Plugins[id]; cur != nil {
				cur.Commit, cur.UpdatedAt = f.commit, now()
			}
			return nil
		}); err != nil {
			return nil, err
		}
		cur := *prev
		cur.Commit = f.commit
		return &cur, nil
	}
	// A version in the manifest holds the plugin on it until the version changes, whatever else moved upstream.
	if prev != nil && version != "" && version == prev.Version && r.marketplace != LocalMarketplace {
		return prev, nil
	}
	label := version
	if label == "" {
		label = f.commit
		if len(label) > 12 {
			label = label[:12]
		}
	}
	short := strings.TrimPrefix(hash, "sha256:")[:12]
	folder := short
	if label != "" {
		folder = regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(label, "-") + "-" + short
	}
	rel := filepath.Join("cache", r.marketplace, r.name, folder)
	dest := filepath.Join(m.Root, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return nil, err
	}
	var out Record
	err = m.update(func(st *stateFile) error {
		for otherID, o := range st.Plugins {
			if otherID != id && o.Name == r.name {
				return fmt.Errorf("a plugin named %s is already installed as %s; remove it first", r.name, otherID)
			}
		}
		if !isDir(dest) {
			if err := os.Rename(f.dir, dest); err != nil {
				return err
			}
		}
		rec := &Record{ID: id, Name: r.name, Marketplace: r.marketplace, Version: label, Commit: f.commit, Hash: hash, Dir: rel, Enabled: true, InstalledAt: now(), UpdatedAt: now(), Overlay: r.entry}
		if r.marketplace == LocalMarketplace {
			rec.Source = r.source
		}
		if de := p.Manifest.DefaultEnabled; de != nil {
			rec.Enabled = *de
		}
		if r.entry != nil && r.entry.DefaultEnabled != nil {
			rec.Enabled = *r.entry.DefaultEnabled
		}
		if cur := st.Plugins[id]; cur != nil {
			rec.Enabled, rec.ApprovedHash, rec.ApprovedAt, rec.Pinned, rec.InstalledAt = cur.Enabled, cur.ApprovedHash, cur.ApprovedAt, cur.Pinned, cur.InstalledAt
			if rec.Version == "" {
				rec.Version = cur.Version
			}
		}
		st.Plugins[id] = rec
		out = *rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.pruneVersions(out)
	return &out, nil
}

// pruneVersions leaves the current version and the one before it: a session opened earlier may still be running that one.
func (m *Manager) pruneVersions(rec Record) {
	parent := filepath.Dir(filepath.Join(m.Root, rec.Dir))
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	type version struct {
		path string
		at   time.Time
	}
	var old []version
	for _, e := range entries {
		p := filepath.Join(parent, e.Name())
		if p == filepath.Join(m.Root, rec.Dir) || !e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			old = append(old, version{p, info.ModTime()})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].at.After(old[j].at) })
	for i, v := range old {
		if i >= 1 {
			_ = os.RemoveAll(v.path)
		}
	}
}

// UpdateResult says what Update did.
type UpdateResult struct {
	Record *Record
	// Changed: new content is in place, and needs approving before it runs.
	Changed bool
	Note    string
}

// Update refreshes a plugin's marketplace and moves it to what that offers now. Pinned plugins stay.
func (m *Manager) Update(ctx context.Context, id string) (*UpdateResult, error) {
	st, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	rec := st.Plugins[id]
	if rec == nil {
		return nil, fmt.Errorf("%s is not installed", id)
	}
	if rec.Pinned {
		return &UpdateResult{Record: rec, Note: id + " is pinned at " + rec.Version + "; unpin it to update."}, nil
	}
	if rec.Marketplace != LocalMarketplace {
		if err := m.UpdateMarketplace(ctx, rec.Marketplace); err != nil {
			return nil, err
		}
	}
	st, err = m.snapshot()
	if err != nil {
		return nil, err
	}
	r, err := m.resolveEntry(st, rec.Name, rec.Marketplace)
	if err != nil {
		return nil, err
	}
	next, err := m.installResolved(ctx, r, rec)
	if err != nil {
		return nil, err
	}
	res := &UpdateResult{Record: next, Changed: next.Hash != rec.Hash}
	if !res.Changed {
		res.Note = id + " is up to date at " + next.Version + "."
	}
	return res, nil
}

// Remove deletes a plugin from the store, with its data folder unless keepData.
func (m *Manager) Remove(id string, keepData bool) error {
	if m.Policy().PluginForced(id) {
		return fmt.Errorf("your organization distributes %s", id)
	}
	var rec *Record
	err := m.update(func(st *stateFile) error {
		rec = st.Plugins[id]
		if rec == nil {
			return fmt.Errorf("%s is not installed", id)
		}
		delete(st.Plugins, id)
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(filepath.Join(m.Root, rec.Dir))); err != nil {
		return err
	}
	if !keepData {
		return os.RemoveAll(m.dataDir(id))
	}
	return nil
}

func (m *Manager) change(id string, f func(*Record) error) error {
	return m.update(func(st *stateFile) error {
		rec := st.Plugins[id]
		if rec == nil {
			return fmt.Errorf("%s is not installed", id)
		}
		return f(rec)
	})
}

// SetEnabled is the user's switch.
func (m *Manager) SetEnabled(id string, enabled bool) error {
	if !enabled && m.Policy().PluginForced(id) {
		return fmt.Errorf("your organization keeps %s on", id)
	}
	if enabled && m.Policy().PluginBlocked(id) {
		return fmt.Errorf("your organization's policy blocks %s", id)
	}
	return m.change(id, func(r *Record) error { r.Enabled = enabled; return nil })
}

// SetPinned keeps a plugin on its version, or lets it move again.
func (m *Manager) SetPinned(id string, pinned bool) error {
	return m.change(id, func(r *Record) error { r.Pinned = pinned; return nil })
}

// Approve lets the content with this hash run on this device. An empty hash means what is installed now; a caller that showed the
// contents to someone passes the hash it showed, so what changed meanwhile isn't approved unseen.
func (m *Manager) Approve(id, hash string) error {
	return m.change(id, func(r *Record) error {
		if hash != "" && hash != r.Hash {
			return fmt.Errorf("%s changed since you looked at it; look again", id)
		}
		current, err := HashTree(filepath.Join(m.Root, r.Dir))
		if err != nil {
			return err
		}
		if current != r.Hash {
			return fmt.Errorf("the files of %s no longer match what was installed; install it again", id)
		}
		r.ApprovedHash, r.ApprovedAt = r.Hash, now()
		return nil
	})
}

// Revoke takes an approval back.
func (m *Manager) Revoke(id string) error {
	return m.change(id, func(r *Record) error { r.ApprovedHash, r.ApprovedAt = "", ""; return nil })
}

// ---- status ----

// Place is where a plugin would run. Without a folder only the plugin's own state counts.
type Place struct {
	Cwd     string
	Trusted bool
}

// Info is a plugin as frontends and commands show it.
type Info struct {
	ID, Name, Marketplace, DisplayName, Description, Version string
	Commit, Hash, Root                                       string
	Status                                                   Status
	Reason                                                   string
	Enabled, Approved, Pinned                                bool
	// Managed: the organization distributes it. SuggestedBy names the project settings asking for it.
	Managed     bool
	SuggestedBy string
	InstalledAt string
	Source      string
	Contents    Contents
	Problems    []string
	// Plugin is the folder read, when it could be.
	Plugin *Plugin
}

func (m *Manager) read(rec *Record) (*Plugin, string, error) {
	root := filepath.Join(m.Root, rec.Dir)
	stamp, err := treeStamp(root)
	if err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	e, ok := m.memo[root]
	m.mu.Unlock()
	if ok && e.stamp == stamp {
		return e.plugin, e.hash, e.err
	}
	hash, herr := HashTree(root)
	if herr != nil {
		return nil, "", herr
	}
	p, err := Load(root, rec.Name, rec.Marketplace, m.dataDir(rec.ID), overlayOf(rec.Overlay))
	m.mu.Lock()
	if m.memo == nil {
		m.memo = map[string]memoEntry{}
	}
	m.memo[root] = memoEntry{stamp: stamp, hash: hash, plugin: p, err: err}
	m.mu.Unlock()
	return p, hash, err
}

func (m *Manager) info(rec *Record, pol Policy, proj projectConfig, place Place, st *stateFile) Info {
	in := Info{ID: rec.ID, Name: rec.Name, Marketplace: rec.Marketplace, Version: rec.Version, Commit: rec.Commit, Hash: rec.Hash, Root: filepath.Join(m.Root, rec.Dir),
		Enabled: rec.Enabled, Pinned: rec.Pinned, Approved: rec.ApprovedHash != "" && rec.ApprovedHash == rec.Hash, Managed: pol.PluginForced(rec.ID), InstalledAt: rec.InstalledAt, Source: rec.Source.String()}
	p, hash, err := m.read(rec)
	if err == nil && p != nil {
		in.Plugin, in.Contents, in.Problems, in.DisplayName, in.Description = p, p.Contents, p.Problems, p.Manifest.DisplayName, p.Manifest.Description
		if rec.Overlay != nil {
			if rec.Overlay.DisplayName != "" {
				in.DisplayName = rec.Overlay.DisplayName
			}
			if rec.Overlay.Description != "" {
				in.Description = rec.Overlay.Description
			}
		}
	}
	set := func(s Status, why string) { in.Status, in.Reason = s, why }
	refusal := ""
	if mk := st.Marketplaces[rec.Marketplace]; mk != nil {
		refusal = pol.SourceRefusal(mk.Source)
	} else if rec.Marketplace == LocalMarketplace {
		refusal = pol.SourceRefusal(rec.Source)
	}
	enabled := rec.Enabled
	if on, ok := proj.enabled[rec.ID]; ok {
		enabled = on
	}
	if pol.PluginForced(rec.ID) {
		enabled = true
	}
	in.Enabled = enabled
	switch {
	case pol.PluginBlocked(rec.ID):
		set(StatusBlockedByPolicy, "Your organization's policy blocks this plugin.")
	case refusal != "":
		set(StatusBlockedByPolicy, refusal)
	case err != nil:
		set(StatusBroken, err.Error())
	case hash != rec.Hash:
		set(StatusChangedOnDisk, "Its files no longer match what was installed. Install it again before anything of it runs.")
	case !enabled:
		set(StatusDisabled, "It is turned off.")
	case !in.Approved:
		set(StatusNeedsApproval, "Nothing of it runs until you approve it on this device.")
	case place.Cwd != "" && !place.Trusted:
		set(StatusUntrustedFolder, "This folder is not trusted on this machine.")
	default:
		set(StatusActive, "")
	}
	return in
}

// List returns the installed plugins as they stand at a place, then what the organization distributes and what the place's
// project asks for that isn't here.
func (m *Manager) List(place Place) ([]Info, error) {
	st, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	pol := m.Policy()
	var proj projectConfig
	if place.Trusted && place.Cwd != "" {
		proj = readProjectConfig(place.Cwd)
	}
	var out []Info
	ids := make([]string, 0, len(st.Plugins))
	for id := range st.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, m.info(st.Plugins[id], pol, proj, place, st))
	}
	missing := func(id string) bool { _, ok := st.Plugins[id]; return !ok }
	forced := make([]string, 0, len(pol.Plugins))
	for id, on := range pol.Plugins {
		if on && missing(id) {
			forced = append(forced, id)
		}
	}
	sort.Strings(forced)
	for _, id := range forced {
		out = append(out, m.absent(id, true, ""))
	}
	suggested := make([]string, 0, len(proj.enabled))
	for id, on := range proj.enabled {
		if on && missing(id) && !pol.Plugins[id] && !pol.PluginBlocked(id) {
			suggested = append(suggested, id)
		}
	}
	sort.Strings(suggested)
	for _, id := range suggested {
		out = append(out, m.absent(id, false, proj.path))
	}
	return out, nil
}

func (m *Manager) absent(id string, managed bool, by string) Info {
	name, market, _ := strings.Cut(id, "@")
	reason := "This project asks for it."
	if managed {
		reason = "Your organization distributes it; it installs in the background, or install it now."
	}
	return Info{ID: id, Name: name, Marketplace: market, Status: StatusNotInstalled, Reason: reason, Managed: managed, SuggestedBy: by}
}

// Find returns one plugin as it stands at a place.
func (m *Manager) Find(id string, place Place) (Info, error) {
	list, err := m.List(place)
	if err != nil {
		return Info{}, err
	}
	for _, in := range list {
		if in.ID == id {
			return in, nil
		}
	}
	return Info{}, fmt.Errorf("%s is not installed", id)
}

// Active returns the plugins that may run at a place: approved here, enabled, allowed, unchanged, and in a trusted folder.
func (m *Manager) Active(place Place) []*Plugin {
	list, err := m.List(place)
	if err != nil {
		return nil
	}
	var out []*Plugin
	for _, in := range list {
		if in.Status == StatusActive && in.Plugin != nil {
			out = append(out, in.Plugin)
		}
	}
	return out
}

// Suggestions are the marketplaces of a trusted project's settings not yet registered.
func (m *Manager) Suggestions(place Place) map[string]Source {
	if !place.Trusted || place.Cwd == "" {
		return nil
	}
	st, err := m.snapshot()
	if err != nil {
		return nil
	}
	out := map[string]Source{}
	for name, s := range readProjectConfig(place.Cwd).marketplaces {
		if st.Marketplaces[name] == nil {
			out[name] = s
		}
	}
	return out
}

// SyncDistributed registers what the organization distributes and fetches the plugins it turns on. It fetches and copies, and runs
// nothing: each device still approves each plugin. A problem with one thing doesn't stop the others.
func (m *Manager) SyncDistributed(ctx context.Context) []error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	pol := m.Policy()
	st, err := m.snapshot()
	if err != nil {
		return []error{err}
	}
	var errs []error
	names := make([]string, 0, len(pol.Distributed))
	for n := range pol.Distributed {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if st.Marketplaces[n] != nil {
			continue
		}
		rec, err := m.AddMarketplace(ctx, pol.Distributed[n])
		if err != nil {
			errs = append(errs, fmt.Errorf("marketplace %s: %w", n, err))
			continue
		}
		if rec.Name != n {
			errs = append(errs, fmt.Errorf("the policy names a marketplace %s, and its file says %s", n, rec.Name))
		}
	}
	ids := make([]string, 0, len(pol.Plugins))
	for id, on := range pol.Plugins {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		if st.Plugins[id] != nil {
			continue
		}
		if _, err := m.Install(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("plugin %s: %w", id, err))
		}
	}
	return errs
}

// projectConfig is what a project's settings files say about plugins.
type projectConfig struct {
	enabled      map[string]bool
	marketplaces map[string]Source
	path         string
}

// readProjectConfig reads the plugin keys of a folder's settings files, highest level first, the first to name a plugin deciding.
// Only for a trusted folder. A repository can't enable what this device did not approve: this only says on or off.
func readProjectConfig(cwd string) projectConfig {
	out := projectConfig{enabled: map[string]bool{}, marketplaces: map[string]Source{}}
	for _, rel := range []string{".harness/settings.local.json", ".claude/settings.local.json", ".harness/settings.json", ".claude/settings.json"} {
		p := filepath.Join(cwd, filepath.FromSlash(rel))
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f struct {
			Enabled map[string]bool `json:"enabledPlugins"`
			Extra   map[string]struct {
				Source Source `json:"source"`
			} `json:"extraKnownMarketplaces"`
		}
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if len(f.Enabled)+len(f.Extra) > 0 && out.path == "" {
			out.path = p
		}
		for id, on := range f.Enabled {
			if _, ok := out.enabled[id]; !ok {
				out.enabled[id] = on
			}
		}
		for n, m := range f.Extra {
			if _, ok := out.marketplaces[n]; !ok {
				out.marketplaces[n] = m.Source
			}
		}
	}
	return out
}
