// Package sandbox runs the commands the model chooses restricted by the operating system, without a VM: the process runs
// directly on the machine, under a policy it cannot leave and its children inherit. The Driver is the only part that
// touches the platform; the policy (policy.go), the network proxy (proxy.go) and the explanation of failures (diagnose.go)
// hold for every driver. Those are sandbox-exec on macOS and bubblewrap or Landlock with seccomp on Linux, chosen by what the
// machine allows; platforms without one, Windows included, have a stub that says so, and the frontends show it as unavailable
// rather than off. docs/design/platforms.md has the differences.
//
// What it is: the boundary permission prompts are not. A shell command can write any file the user can and hide its target
// from any parser, so the permission engine only sees what the command line shows (see permissions/computer.go). Here the
// kernel checks the file the process actually opens.
//
// What it is not: file tools (Read, Write, Edit) run in the client and have the permission engine; MCP servers, hooks and
// external ACP agents are programs the user configured. And the model's work survives the sandbox: files it writes in a
// project or a toolchain cache are run later by the user, outside it.
package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Kind is the policy a command runs under.
type Kind int

const (
	// KindCommand is a command of the model: Bash, Monitor, TerminalRun.
	KindCommand Kind = iota
	// KindServer is a dev server the user asked to preview. The same files and hosts, but it may listen and reach what
	// listens on this machine (its database, its own other processes): a server that cannot bind a port is no server.
	KindServer
)

// Info describes the driver of this platform.
type Info struct {
	// Platform is "macos", "linux", "windows"...
	Platform string
	// Supported is false for the stubs of platforms without a driver yet, and on a machine that does not allow the driver;
	// Reason says so.
	Supported bool
	// Reason, when Supported, is what the sandbox cannot hold on this machine, for the settings screen.
	Reason string
}

// Driver confines a process on one platform.
type Driver interface {
	Info() Info
	// Command returns the command line that runs argv under the policy, and its children with it. It fails when the
	// policy cannot be applied: nothing runs then.
	Command(p Policy, argv []string) ([]string, error)
}

// Reporter is implemented by drivers that can tell afterwards what they refused a command: operation and target, as "op
// target", for the refusals tagged Policy.Tag since a time.
type Reporter interface {
	Refusals(tag string, since time.Time) []string
}

// Preparer is implemented by drivers that need more than the policy says: Wrap calls it after building the policy, before
// Command. proxySocket returns the path of a unix socket that serves the session's proxy, starting it on first use.
type Preparer interface {
	Prepare(p *Policy, proxySocket func() (string, error)) error
}

// ErrUnsupported is what the stubs return.
var ErrUnsupported = errors.New("the command sandbox is not supported on this platform yet")

// Settings is the user's choice as the settings hierarchy resolved it, read again for every command.
type Settings struct {
	// Enabled is sandbox.enabled.
	Enabled bool
	// Forced says a policy document requires it: no setting turns it off, and on a platform without a driver commands
	// are refused instead of running without it.
	Forced bool
	// AllowedHosts are added to DefaultHosts, AllowedWritePaths to the writable roots. Protected paths win over them.
	AllowedHosts      []string
	AllowedWritePaths []string
	// AllowLocalhost lets commands listen on and reach the loopback interface, which tests and dev servers need. It also
	// reaches every other service on this machine, so a user can turn it off. On by default in settings.
	AllowLocalhost bool
	// AutoAllowBash lets a sandboxed command run without a permission prompt where it would otherwise ask (deny and ask
	// rules still apply). Off by default.
	AutoAllowBash bool
	// OutsideWithoutPrompt drops the user-only prompt for running outside the sandbox: the normal check of the command
	// applies, which bypass mode then passes. Off by default.
	OutsideWithoutPrompt bool
	// OutsideRefused comes from a policy document: running outside the sandbox is not offered at all.
	OutsideRefused bool
}

// Session is the sandbox of one session: its settings, and the proxy its commands go through. A nil Session runs
// everything unconfined, which is what tests and hosts without a session get.
type Session struct {
	// Driver defaults to NewDriver.
	Driver Driver
	// Settings returns the current settings.
	Settings func() Settings
	// Home and HarnessHome are the user's home and the harness data directory.
	Home, HarnessHome string
	// Getenv reads the client's environment (default os.Getenv).
	Getenv func(string) string

	once   sync.Once
	driver Driver
	mu     sync.Mutex
	proxy  *proxy
	// keepProxy: a preview server outlives the session and still needs its proxy.
	keepProxy bool
}

func (s *Session) getDriver() Driver {
	s.once.Do(func() {
		s.driver = s.Driver
		if s.driver == nil {
			s.driver = NewDriver()
		}
	})
	return s.driver
}

func (s *Session) settings() Settings {
	if s.Settings == nil {
		return Settings{}
	}
	return s.Settings()
}

// State is what the permission engine and the tools need to know before a command runs.
type State struct {
	// Active: commands run confined.
	Active bool
	// Refusing: the sandbox is required and cannot be applied here, so commands are refused. Reason says why.
	Refusing bool
	// Reason says why the sandbox is not applied although it is on.
	Reason   string
	Settings Settings
}

// State returns the current state.
func (s *Session) State() State {
	if s == nil {
		return State{}
	}
	st := s.settings()
	if !st.Enabled && !st.Forced {
		return State{Settings: st}
	}
	info := s.getDriver().Info()
	if !info.Supported {
		if st.Forced {
			return State{Refusing: true, Settings: st, Reason: "your administrator requires the command sandbox, which this machine does not have: " + info.Reason}
		}
		return State{Settings: st, Reason: info.Reason}
	}
	return State{Active: true, Settings: st}
}

// Request says what one command is.
type Request struct {
	Kind Kind
	// WorkDirs are the session's working directories now.
	WorkDirs []string
	// ScratchDir is the session's (or subagent's) scratch directory.
	ScratchDir string
}

// Wrapped is a command ready to start under the sandbox.
type Wrapped struct {
	// Argv is the whole command line, sandbox first.
	Argv []string
	// Env is added after the rest of the command's environment.
	Env []string
	// Policy is what applies, for explaining failures.
	Policy Policy

	proxy   *proxy
	since   uint64
	driver  Driver
	started time.Time
	guard   *guard
}

// Wrap returns argv under the sandbox. It returns nil without an error when no sandbox applies: the command runs as is.
// An error means the command must not run.
func (s *Session) Wrap(req Request, argv []string) (*Wrapped, error) {
	if s == nil {
		return nil, nil
	}
	state := s.State()
	if state.Refusing {
		return nil, errors.New(state.Reason)
	}
	if !state.Active {
		return nil, nil
	}
	if req.ScratchDir == "" {
		return nil, errors.New("the command sandbox needs a scratch directory")
	}
	px, err := s.startProxy(req.Kind)
	if err != nil {
		return nil, fmt.Errorf("the command sandbox could not start its network proxy: %w", err)
	}
	tmp := filepath.Join(req.ScratchDir, "tmp")
	template := filepath.Join(req.ScratchDir, "git-template")
	for _, d := range []string{tmp, template} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("the command sandbox could not create %s: %w", d, err)
		}
	}
	st := state.Settings
	p := BuildPolicy(PolicyInput{
		Home: s.Home, HarnessHome: s.HarnessHome, WorkDirs: req.WorkDirs, ScratchDir: req.ScratchDir, TempDir: tmp,
		ExtraWrite: st.AllowedWritePaths, Getenv: s.Getenv, ProxyPort: px.port(),
		Localhost: st.AllowLocalhost, Server: req.Kind == KindServer,
	})
	p.Tag = newTag()
	if pr, ok := s.getDriver().(Preparer); ok {
		if err := pr.Prepare(&p, px.unixSocket); err != nil {
			return nil, fmt.Errorf("the command sandbox could not be prepared: %w", err)
		}
	}
	full, err := s.getDriver().Command(p, argv)
	if err != nil {
		return nil, err
	}
	w := &Wrapped{Argv: full, Env: commandEnv(px.port(), tmp, template), Policy: p, proxy: px, since: px.mark(), driver: s.getDriver(), started: time.Now()}
	if p.GuardNames {
		w.guard = newGuard(p, req.ScratchDir)
	}
	return w, nil
}

func newTag() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "harness-sandbox-" + hex.EncodeToString(b)
}

// commandEnv is the environment that points tools at the proxy and at what they may write. The proxy variables come in
// both spellings since tools read one or the other; NODE_USE_ENV_PROXY makes Node's own fetch honor them. An empty git
// template makes git init and git clone work without a hooks directory, which they could not create.
func commandEnv(port int, tmp, template string) []string {
	addr := "http://127.0.0.1:" + strconv.Itoa(port)
	env := []string{"HARNESS_SANDBOX=1", "TMPDIR=" + tmp, "GIT_TEMPLATE_DIR=" + template, "NODE_USE_ENV_PROXY=1"}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		env = append(env, k+"="+addr)
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		env = append(env, k+"=localhost,127.0.0.1,::1")
	}
	return env
}

// startProxy returns the session's proxy, started on first use.
func (s *Session) startProxy(kind Kind) (*proxy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == KindServer {
		s.keepProxy = true
	}
	if s.proxy != nil {
		return s.proxy, nil
	}
	px, err := startProxy(func() Hosts { return NewHosts(append(slices.Clone(DefaultHosts), s.settings().AllowedHosts...)) })
	if err != nil {
		return nil, err
	}
	s.proxy = px
	return px, nil
}

// Close ends the session's proxy, unless a preview server still uses it (those can outlive the session).
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proxy != nil && !s.keepProxy {
		s.proxy.close()
		s.proxy = nil
	}
}
