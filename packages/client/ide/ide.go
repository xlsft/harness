// Package ide reaches an editor extension from the client: it finds the editor of a session through lock files, and
// calls harness.ide.v1.EditorBridgeService on it. The design is in docs/design/ide.md. Lock files, ports and tokens are
// local to this device and never synced.
package ide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	idev1 "github.com/xlsft/harness/packages/client/gen/harness/ide/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/ide/v1/idev1connect"
)

// LockEnv names the lock file of the editor that started this process or its terminal. It wins over the folder match.
const LockEnv = "HARNESS_IDE_LOCK"

const (
	// pingTimeout bounds the check that an editor still answers.
	pingTimeout = 2 * time.Second
	// pingValid is how long an answer is believed, so that a look at the tools does not cost a request.
	pingValid = 10 * time.Second
	// callTimeout bounds the short calls; ShowDiff waits for the user and has no such bound.
	callTimeout = 5 * time.Second
)

// Lock is what an editor window announces in $HARNESS_HOME/ide/.
type Lock struct {
	Port    int      `json:"port"`
	Token   string   `json:"token"`
	PID     int      `json:"pid"`
	Name    string   `json:"name"`
	Folders []string `json:"folders"`

	path string
}

// Dir is where lock files are.
func Dir() string { return filepath.Join(config.Home(), "ide") }

// readLock reads one lock file. It refuses a file others can read or change: the token in it opens the editor.
func readLock(path string) (*Lock, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("mode %o: others can read it", st.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	if l.Port <= 0 || l.Port > 65535 || l.Token == "" {
		return nil, errors.New("no port or token")
	}
	l.path = path
	return &l, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = p.Release() }()
	if runtime.GOOS == "windows" {
		return true // FindProcess opened it, so it runs
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// resolved is path with its symlinks followed, as far as they exist (on macOS the temp dir is behind one).
func resolved(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// within reports whether path is dir or below it, and how long the match is.
func within(dir, path string) (int, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, false
	}
	return len(dir), true
}

// pick chooses the editor for cwd: the pinned lock when it is good, else the longest folder containing cwd.
// Locks of dead processes are removed on the way.
func pick(dir, pinned, cwd string) *Lock {
	if pinned != "" {
		if l, err := readLock(pinned); err == nil && processAlive(l.PID) {
			return l
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	cwd = resolved(cwd)
	var best *Lock
	bestLen := -1
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		l, err := readLock(path)
		if err != nil {
			continue
		}
		if !processAlive(l.PID) {
			_ = os.Remove(path)
			continue
		}
		for _, f := range l.Folders {
			if n, ok := within(resolved(f), cwd); ok && n > bestLen {
				best, bestLen = l, n
			}
		}
	}
	return best
}

// Manager finds the editors of this process's sessions. The zero value is ready; a nil Manager finds none.
type Manager struct {
	mu      sync.Mutex
	editors map[string]*Editor // by lock path and token
}

// For returns the editor that answers for a session in cwd, or nil.
func (m *Manager) For(ctx context.Context, cwd string) *Editor {
	if m == nil {
		return nil
	}
	l := pick(Dir(), os.Getenv(LockEnv), cwd)
	if l == nil {
		return nil
	}
	key := l.path + "\x00" + l.Token + "\x00" + strconv.Itoa(l.Port)
	m.mu.Lock()
	if m.editors == nil {
		m.editors = map[string]*Editor{}
	}
	ed := m.editors[key]
	if ed == nil {
		ed = newEditor(l)
		m.editors[key] = ed
	}
	m.mu.Unlock()
	if !ed.alive(ctx) {
		return nil
	}
	return ed
}

// Editor is one editor window.
type Editor struct {
	lock   *Lock
	client idev1connect.EditorBridgeServiceClient

	mu       sync.Mutex
	name     string
	checked  time.Time
	answered bool
}

// tokenTransport adds the bearer token. The transport is the editor's alone: no proxy from the environment may see
// a request to loopback, and redirects are never followed.
type tokenTransport struct {
	token string
	next  http.RoundTripper
}

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(r)
}

func newEditor(l *Lock) *Editor {
	httpClient := &http.Client{
		Transport:     tokenTransport{token: l.Token, next: &http.Transport{Proxy: nil, DisableCompression: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// Always this address: a lock file names a port, never a host.
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port))
	return &Editor{lock: l, name: l.Name, client: idev1connect.NewEditorBridgeServiceClient(httpClient, url, connect.WithProtoJSON())}
}

func (e *Editor) alive(ctx context.Context) bool {
	e.mu.Lock()
	if time.Since(e.checked) < pingValid {
		ok := e.answered
		e.mu.Unlock()
		return ok
	}
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	resp, err := e.client.Ping(ctx, connect.NewRequest(&idev1.PingRequest{}))
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checked, e.answered = time.Now(), err == nil
	if err == nil && resp.Msg.GetName() != "" {
		e.name = resp.Msg.GetName()
	}
	return e.answered
}

// Name is what the editor calls itself.
func (e *Editor) Name() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.name
}

// ShowDiff opens a proposed change and waits for the user. Cancelling ctx closes the diff.
func (e *Editor) ShowDiff(ctx context.Context, req *idev1.ShowDiffRequest) (*idev1.ShowDiffResponse, error) {
	resp, err := e.client.ShowDiff(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Diagnostics reads the editor's problems.
func (e *Editor) Diagnostics(ctx context.Context, paths []string) ([]*idev1.Diagnostic, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := e.client.GetDiagnostics(ctx, connect.NewRequest(&idev1.GetDiagnosticsRequest{Paths: paths}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetDiagnostics(), nil
}

// OpenFile opens a file at a line (0: wherever it was).
func (e *Editor) OpenFile(ctx context.Context, path string, line, endLine int) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := e.client.OpenFile(ctx, connect.NewRequest(&idev1.OpenFileRequest{Path: path, Line: int32(line), EndLine: int32(endLine)}))
	return err
}

// ReadBuffer returns a file's text as the editor has it; open is false when it does not have it open.
func (e *Editor) ReadBuffer(ctx context.Context, path string) (content string, open bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := e.client.ReadBuffer(ctx, connect.NewRequest(&idev1.ReadBufferRequest{Path: path}))
	if err != nil {
		return "", false, err
	}
	return resp.Msg.GetContent(), resp.Msg.GetOpen(), nil
}

// EditApplied tells the editor a file tool changed a file. Nothing depends on it, so errors are dropped.
func (e *Editor) EditApplied(ctx context.Context, sessionID, path string, reviewed bool) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, _ = e.client.EditApplied(ctx, connect.NewRequest(&idev1.EditAppliedRequest{SessionId: sessionID, Path: path, Reviewed: reviewed}))
}
