// Package lsp gives the model what language servers know: definitions, references, hover, symbols and, after edits,
// errors. A Manager belongs to one session folder. It starts the server a file needs on first use, one per language
// root, and stops them when they are idle, when the session closes and when it moves to another folder.
//
// What is installed is looked up on PATH; nothing is installed for the user. Language servers run project code (build
// scripts, compiler plugins, the project's own TypeScript), so reading a file through this package must never be what
// starts them on a folder the user did not trust: a server starts only for files inside a working directory, and only
// when that directory is trusted. Which binary runs comes from the built-in table or the lspServers setting, which
// project files can set only in trusted workspaces (package settings).
package lsp

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
)

// Defaults of Options.
const (
	DefaultRequestTimeout = 30 * time.Second
	DefaultEditWait       = 800 * time.Millisecond
	DefaultLateWait       = 10 * time.Second
	DefaultIdleTimeout    = 15 * time.Minute
	DefaultMaxRestarts    = 3
	DefaultRestartWindow  = 10 * time.Minute
	configCacheFor        = 5 * time.Second
	publishSettle         = 150 * time.Millisecond
)

// Config is what the settings say at the moment.
type Config struct {
	// Enabled offers the tool and allows diagnostics (lspEnabled).
	Enabled bool
	// Diagnostics reports errors after edits (lspDiagnostics).
	Diagnostics bool
	// StartForDiagnostics lets an edit start a server (lspDiagnosticsStartServer); otherwise only one that runs reports.
	StartForDiagnostics bool
	// Servers are the command overrides and added languages (lspServers).
	Servers map[string]settings.LSPServer
}

// Options configure a Manager. Zero durations and counts take the defaults.
type Options struct {
	// Config reads the settings; it is called at most every few seconds. Nil: enabled, no overrides.
	Config func() Config
	// Trusted reports whether a working directory is trusted, which starting a server there requires. Nil: none is.
	Trusted func(dir string) bool
	// ReadDenied reports whether a permission rule denies reading a path. What such a file holds is left out of results.
	ReadDenied func(path string) bool
	Logger     *slog.Logger

	// RequestTimeout bounds one LSP call, a server's start included.
	RequestTimeout time.Duration
	// EditWait bounds how long an edit waits for diagnostics; LateWait is how long after it errors are still collected
	// for a later request.
	EditWait, LateWait time.Duration
	// IdleTimeout stops a server nothing used for this long.
	IdleTimeout time.Duration
	// MaxRestarts is how many crashes within RestartWindow a language root survives.
	MaxRestarts   int
	RestartWindow time.Duration
}

// Manager runs the language servers of a session.
type Manager struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	entries  map[string]*entry
	closed   bool
	reaping  bool
	stopReap chan struct{}
	cfg      Config
	cfgAt    time.Time
}

// entry is a language root's server, across its restarts.
type entry struct {
	spec    *Spec
	root    string
	srv     *server
	counted *server // the last server whose crash is on record
	crashes []time.Time
	failure string
}

var _ tools.EditWatcher = (*Manager)(nil)

// New creates a Manager; nothing runs until a tool call or an edit needs it.
func New(o Options) *Manager {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.EditWait <= 0 {
		o.EditWait = DefaultEditWait
	}
	if o.LateWait <= 0 {
		o.LateWait = DefaultLateWait
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.MaxRestarts <= 0 {
		o.MaxRestarts = DefaultMaxRestarts
	}
	if o.RestartWindow <= 0 {
		o.RestartWindow = DefaultRestartWindow
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Manager{opts: o, log: log, entries: map[string]*entry{}, stopReap: make(chan struct{})}
}

func (m *Manager) config() Config {
	if m == nil || m.opts.Config == nil {
		return Config{Enabled: true, Diagnostics: true}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfgAt.IsZero() || time.Since(m.cfgAt) > configCacheFor {
		m.cfg, m.cfgAt = m.opts.Config(), time.Now()
	}
	return m.cfg
}

var errClosed = errors.New("the session has closed")

// refusedError is a reason not to ask a server at all, in words for the model.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }

// place is where a call comes from: its agent's folders and environment.
type place struct {
	cwd      string
	dirs     []string
	realDirs []string
	environ  []string
}

func placeOf(env *tools.Env) place {
	p := place{cwd: env.CurrentDir(), dirs: env.WorkingDirs()}
	for _, d := range p.dirs {
		p.realDirs = append(p.realDirs, realPath(d))
	}
	p.environ = append(os.Environ(), env.ExtraEnv()...)
	return p
}

// acquire returns the server of a file. With start false only a server that already runs or that crashed is considered
// (nil, nil when there is none).
func (m *Manager) acquire(spec *Spec, file string, at place, start bool) (*server, error) {
	root, workDir := findRoot(file, spec.RootMarkers, at.realDirs)
	key := spec.ID + "\x00" + root
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errClosed
	}
	e := m.entries[key]
	if e != nil && e.srv != nil {
		if e.srv.alive() {
			e.srv.touch()
			return e.srv, nil
		}
		m.recordCrashLocked(e, e.srv)
	}
	if workDir == "" {
		// A file outside the working directories, such as a dependency a definition led to: a running server of the
		// language can answer about it; reading it does not start one over there.
		if srv := m.runningLocked(spec); srv != nil {
			srv.touch()
			return srv, nil
		}
		if !start {
			return nil, nil
		}
		return nil, &refusedError{fmt.Sprintf("%s is outside the working directories and no language server of this kind runs yet, so none can answer about it; a server starts only for files in a working directory.", file)}
	}
	if e == nil && !start {
		return nil, nil
	}
	if m.opts.Trusted == nil || !m.opts.Trusted(workDir) {
		if !start {
			return nil, nil
		}
		return nil, &refusedError{fmt.Sprintf("%s is not a trusted workspace. Language servers run project code, so none starts here until the user trusts the folder; use Grep and Read.", workDir)}
	}
	if e != nil {
		now := time.Now()
		e.crashes = slices.DeleteFunc(e.crashes, func(t time.Time) bool { return now.Sub(t) > m.opts.RestartWindow })
		if len(e.crashes) > m.opts.MaxRestarts {
			return nil, &refusedError{fmt.Sprintf("The %s language server for %s crashed %d times in a row and is not restarted again for now (%s). Use Grep and Read.", spec.ID, root, len(e.crashes), e.failure)}
		}
	}
	name, bin, args, err := spec.resolve(at.environ, filepath.Ext(file))
	if err != nil {
		return nil, err
	}
	srv, err := startServer(spec, root, name, bin, args, at.environ)
	if err != nil {
		if e == nil {
			e = &entry{spec: spec, root: root}
			m.entries[key] = e
		}
		e.crashes = append(e.crashes, time.Now())
		e.failure = err.Error()
		return nil, fmt.Errorf("could not start %s: %w", name, err)
	}
	if e == nil {
		e = &entry{spec: spec, root: root}
		m.entries[key] = e
	}
	e.srv = srv
	m.log.Debug("language server started", "server", name, "root", root)
	go func() {
		<-srv.done
		m.mu.Lock()
		defer m.mu.Unlock()
		m.recordCrashLocked(e, srv)
	}()
	m.startReaperLocked()
	return srv, nil
}

func (m *Manager) recordCrashLocked(e *entry, srv *server) {
	if srv.stopping.Load() || e.counted == srv {
		return
	}
	e.counted = srv
	e.crashes = append(e.crashes, time.Now())
	e.failure = srv.failure()
	m.log.Warn("language server crashed", "server", srv.name, "root", srv.root, "detail", e.failure)
}

func (m *Manager) runningLocked(spec *Spec) *server {
	for _, e := range m.entries {
		if e.spec.ID == spec.ID && e.srv != nil && e.srv.alive() {
			return e.srv
		}
	}
	return nil
}

func (m *Manager) startReaperLocked() {
	if m.reaping {
		return
	}
	m.reaping = true
	stop := m.stopReap
	every := min(m.opts.IdleTimeout/4, time.Minute)
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.reap()
			}
		}
	}()
}

// reap stops what nothing used for IdleTimeout, or everything once the setting is off.
func (m *Manager) reap() {
	off := !m.config().Enabled
	var stale []*server
	m.mu.Lock()
	for key, e := range m.entries {
		if e.srv == nil || !e.srv.alive() {
			if len(e.crashes) == 0 || time.Since(e.crashes[len(e.crashes)-1]) > m.opts.RestartWindow {
				delete(m.entries, key)
			}
			continue
		}
		if off || time.Since(e.srv.idleSince()) > m.opts.IdleTimeout {
			stale = append(stale, e.srv)
			delete(m.entries, key)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		go s.stop()
	}
}

// Reset forgets every server and stops them in the background: the session moved to another folder, in the middle of a
// tool call. The Manager stays usable there.
func (m *Manager) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	servers := m.detachLocked()
	m.mu.Unlock()
	for _, s := range servers {
		go s.stop()
	}
}

// Close stops every server for good and waits for them; the session closes.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.stopReap)
	}
	servers := m.detachLocked()
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stop()
		}()
	}
	wg.Wait()
}

func (m *Manager) detachLocked() []*server {
	servers := make([]*server, 0, len(m.entries))
	for _, e := range m.entries {
		if e.srv != nil {
			servers = append(servers, e.srv)
		}
	}
	m.entries = map[string]*entry{}
	return servers
}

// servers returns the running servers.
func (m *Manager) servers() []*server {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*server, 0, len(m.entries))
	for _, e := range m.entries {
		if e.srv != nil && e.srv.alive() {
			out = append(out, e.srv)
		}
	}
	return out
}

func (m *Manager) readDenied(path string) bool {
	return m.opts.ReadDenied != nil && m.opts.ReadDenied(path)
}

// sessionPath maps a real path from a server back to the working directory it lies in, as the session names it (the
// two differ when a working directory is reached through a symlink): that is what rules and the model know.
func sessionPath(path string, dirs []string) (string, bool) {
	for _, d := range dirs {
		if isInside(path, d) {
			return path, true
		}
		if real := realPath(d); isInside(path, real) {
			rel, _ := filepath.Rel(real, path)
			return filepath.Join(d, rel), true
		}
	}
	return path, false
}

// displayPath is the path relative to cwd, or absolute outside it.
func displayPath(path, cwd string) string {
	if isInside(path, cwd) {
		if rel, err := filepath.Rel(cwd, path); err == nil {
			return rel
		}
	}
	return path
}
