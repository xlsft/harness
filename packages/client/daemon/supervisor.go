package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/config"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

// A team host (docs/design/hosts.md, decisions 6 and 7): this process holds a token that can do nothing but ask for
// workers, and starts one child per member under a Unix user of the member's own. It reads no repository and runs no agent.

// Worker is a started worker process.
type Worker interface {
	// Wait returns when the process has exited.
	Wait() error
	// Stop asks it to end, and kills it if it doesn't.
	Stop()
}

// WorkerSpec is what a Launcher starts.
type WorkerSpec struct {
	UserID, DeviceID string
	// Token is the worker's device token. It goes over a pipe and is in no environment or argv.
	Token string
	// UID and Home are the worker's: Home is created for it, 0700.
	UID, GID int
	Home     string
}

// Launcher starts workers. UIDLauncher is the real one.
type Launcher interface {
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
}

// SupervisorOptions configure a Supervisor.
type SupervisorOptions struct {
	ServerURL string
	HTTP      *http.Client
	Tokens    rpc.Tokens
	Logger    *slog.Logger
	Launcher  Launcher
	Name      string
	// BaseDir holds the supervisor's records and the workers' homes.
	BaseDir string
	// UIDBase and UIDCount are the Unix users the workers run as. Nothing else on the machine may use them.
	UIDBase, UIDCount int
	MaxSessions       int
	SandboxDriver     string
	ReportEvery       time.Duration
	PollSeconds       int32
	// RestartDelay is the pause before starting a worker that exited (default 2 s).
	RestartDelay time.Duration
}

// Supervisor keeps the wanted workers running.
type Supervisor struct {
	o   SupervisorOptions
	api relayv1connect.HostAgentServiceClient
	log *slog.Logger

	mu      sync.Mutex
	running map[string]*runningWorker
}

type runningWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// ErrSharedUnavailable says why this machine can't be a shared host.
var ErrSharedUnavailable = errors.New("a shared host needs Linux and a daemon running as root, to run each user's worker under a Unix user of its own; without that, give each user a host of their own")

// CheckShared reports whether this process can separate users.
func CheckShared() error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return ErrSharedUnavailable
	}
	return nil
}

// NewSupervisor makes a Supervisor; Run starts it.
func NewSupervisor(o SupervisorOptions) *Supervisor {
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	if o.ReportEvery <= 0 {
		o.ReportEvery = 30 * time.Second
	}
	if o.PollSeconds <= 0 {
		o.PollSeconds = 25
	}
	if o.RestartDelay <= 0 {
		o.RestartDelay = 2 * time.Second
	}
	if o.UIDCount <= 0 {
		o.UIDCount = 1000
	}
	if o.HTTP == nil {
		o.HTTP = rpc.HTTPClient(o.ServerURL)
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{
		o: o, log: log.With("component", "supervisor"), running: map[string]*runningWorker{},
		api: relayv1connect.NewHostAgentServiceClient(o.HTTP, strings.TrimRight(o.ServerURL, "/"), rpc.ClientOptions()...),
	}
}

// Run registers and manages workers until ctx ends. It stops them on the way out.
func (s *Supervisor) Run(ctx context.Context) error {
	// Workers go through here to their homes, and read nothing in it: 0711, and the records are 0600.
	if err := os.MkdirAll(s.o.BaseDir, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(s.o.BaseDir, 0o711); err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		res, err := callAPI(ctx, s.o.Tokens, s.api.RegisterAgentHost, &relayv1.RegisterAgentHostRequest{
			Name: s.o.Name, Isolation: relayv1.AgentHostIsolation_AGENT_HOST_ISOLATION_UID, SandboxDriver: s.o.SandboxDriver, Os: runtime.GOOS, Arch: runtime.GOARCH,
			ClientVersion: config.Version, CpuCount: int32(runtime.NumCPU()), MaxSessions: int32(s.o.MaxSessions),
		})
		if err == nil {
			if !res.GetSupervisor() {
				return errors.New("this token is a personal host's: run the daemon without --shared")
			}
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("registering with the server failed; retrying", "err", err)
		if !sleep(ctx, backoff(attempt)) {
			return ctx.Err()
		}
	}
	defer s.stopAll()
	go s.reportLoop(ctx)
	failures := 0
	for ctx.Err() == nil {
		res, err := callAPI(ctx, s.o.Tokens, s.api.ListWantedWorkers, &relayv1.ListWantedWorkersRequest{WaitSeconds: s.o.PollSeconds})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.log.Debug("asking for workers failed", "err", err)
			failures++
			sleep(ctx, backoff(failures))
			continue
		}
		failures = 0
		for _, u := range res.GetStopUserIds() {
			s.stop(u)
		}
		for _, w := range res.GetWorkers() {
			s.ensure(ctx, w.GetUserId())
		}
	}
	return ctx.Err()
}

func (s *Supervisor) reportLoop(ctx context.Context) {
	t := time.NewTicker(s.o.ReportEvery)
	defer t.Stop()
	for {
		// Workers report the sessions; this reports the machine.
		if _, err := callAPI(ctx, s.o.Tokens, s.api.ReportAgentHost, &relayv1.ReportAgentHostRequest{LoadAverage: loadAverage(), DiskFreeBytes: diskFree(s.o.BaseDir)}); err != nil && ctx.Err() == nil {
			s.log.Debug("reporting failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ensure starts a member's worker if none runs, and keeps it running until it is stopped.
func (s *Supervisor) ensure(ctx context.Context, userID string) {
	s.mu.Lock()
	if s.running[userID] != nil {
		s.mu.Unlock()
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	rw := &runningWorker{cancel: cancel, done: make(chan struct{})}
	s.running[userID] = rw
	s.mu.Unlock()
	go func() {
		defer close(rw.done)
		for attempt := 0; cctx.Err() == nil; attempt++ {
			healthy, err := s.runOnce(cctx, userID)
			if cctx.Err() != nil {
				return
			}
			if err != nil {
				s.log.Warn("a worker failed", "user", userID, "err", err)
			}
			if healthy {
				attempt = 0
			}
			if !sleep(cctx, s.o.RestartDelay+backoff(attempt)) {
				return
			}
		}
	}()
}

// runOnce gets a fresh token, starts the worker and waits for it. It reports whether it ran for a while.
func (s *Supervisor) runOnce(ctx context.Context, userID string) (bool, error) {
	uid, err := s.uidOf(userID)
	if err != nil {
		return false, err
	}
	res, err := callAPI(ctx, s.o.Tokens, s.api.IssueWorkerToken, &relayv1.IssueWorkerTokenRequest{UserId: userID})
	if err != nil {
		return false, err
	}
	spec := WorkerSpec{UserID: userID, DeviceID: res.GetDeviceId(), Token: res.GetDeviceToken(), UID: uid, GID: uid, Home: filepath.Join(s.o.BaseDir, "workers", fmt.Sprintf("u%d", uid))}
	w, err := s.o.Launcher.Start(ctx, spec)
	if err != nil {
		return false, err
	}
	started := time.Now()
	waited := make(chan error, 1)
	go func() { waited <- w.Wait() }()
	select {
	case err := <-waited:
		return time.Since(started) > time.Minute, err
	case <-ctx.Done():
		w.Stop()
		<-waited
		return true, nil
	}
}

func (s *Supervisor) stop(userID string) {
	s.mu.Lock()
	rw := s.running[userID]
	delete(s.running, userID)
	s.mu.Unlock()
	if rw != nil {
		rw.cancel()
		<-rw.done
	}
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.stop(id)
	}
}

// uidOf is a member's Unix user on this host: assigned once and kept, so that their files stay theirs.
func (s *Supervisor) uidOf(userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.o.BaseDir, "uids.json")
	uids := map[string]int{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &uids); err != nil {
			return 0, fmt.Errorf("the record of assigned users can't be read: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if uid, ok := uids[userID]; ok {
		return uid, nil
	}
	used := make(map[int]bool, len(uids))
	for _, uid := range uids {
		used[uid] = true
	}
	for uid := s.o.UIDBase; uid < s.o.UIDBase+s.o.UIDCount; uid++ {
		if used[uid] {
			continue
		}
		uids[userID] = uid
		b, err := json.Marshal(uids)
		if err != nil {
			return 0, err
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return 0, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return 0, err
		}
		return uid, nil
	}
	return 0, fmt.Errorf("all %d Unix users of this host are assigned", s.o.UIDCount)
}

// UIDLauncher starts `harness-client daemon worker` as the member's Unix user. Its environment is built from nothing:
// what the supervisor holds stays with the supervisor.
type UIDLauncher struct {
	// Executable is this binary.
	Executable string
	ServerURL  string
	Name       string
	Args       []string
	Logger     *slog.Logger
}

// workerEnv is all a worker's process starts with.
func workerEnv(serverURL, home string) []string {
	env := []string{
		"HOME=" + home,
		// These Unix users are numbers with no passwd entry; programs that ask who they are read this.
		"USER=harness-worker",
		"LOGNAME=harness-worker",
		"HARNESS_HOME=" + filepath.Join(home, ".harness"),
		"HARNESS_SERVER_URL=" + serverURL,
		"HARNESS_KEYRING=0",
		"TMPDIR=" + filepath.Join(home, "tmp"),
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "HARNESS_DEBUG":
			env = append(env, kv)
		}
	}
	return env
}

// Start implements Launcher.
func (l UIDLauncher) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := prepareHome(spec.Home, spec.UID, spec.GID); err != nil {
		return nil, fmt.Errorf("the worker's home: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	args := append([]string{"daemon", "worker", "--name", l.Name, "--root", filepath.Join(spec.Home, "workspaces")}, l.Args...)
	cmd := exec.Command(l.Executable, args...)
	cmd.Env = workerEnv(l.ServerURL, spec.Home)
	cmd.Dir = spec.Home
	cmd.Stdout = logWriter{l.Logger, spec.UserID}
	cmd.Stderr = logWriter{l.Logger, spec.UserID}
	cmd.ExtraFiles = []*os.File{r} // fd 3
	cmd.SysProcAttr = workerAttr(spec.UID, spec.GID)
	if err := cmd.Start(); err != nil {
		w.Close()
		return nil, err
	}
	_, werr := io.WriteString(w, spec.Token)
	w.Close()
	if werr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, werr
	}
	return &procWorker{cmd: cmd}, nil
}

// logWriter passes a worker's log to the supervisor's, a line at a time. Workers log no secrets, so there is none to pass.
type logWriter struct {
	log  *slog.Logger
	user string
}

func (l logWriter) Write(b []byte) (int, error) {
	if l.log != nil {
		l.log.Debug("worker", "user", l.user, "line", strings.TrimRight(string(b), "\n"))
	}
	return len(b), nil
}

type procWorker struct {
	cmd *exec.Cmd
}

func (w *procWorker) Wait() error { return w.cmd.Wait() }

func (w *procWorker) Stop() {
	terminate(w.cmd.Process)
	// Wait, which the supervisor is in, returns as soon as the process is gone; a kill after that finds nothing.
	time.AfterFunc(20*time.Second, func() { _ = w.cmd.Process.Kill() })
}
