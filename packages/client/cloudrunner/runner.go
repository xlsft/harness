package cloudrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/sandbox"
)

// Defaults of a runner.
const (
	DefaultNetwork = "harness-cloud"
	// ProxyAlias is the egress proxy's name on the network when the runner runs in a container.
	ProxyAlias       = "harness-egress"
	DefaultProxyPort = 3128
	// AgentUID is the user the containers run as (deploy/Dockerfile.agent).
	AgentUID = 10001
	// TokenPath is where the container's daemon finds its device token, and deletes it.
	TokenPath = "/run/harness/token"
)

// Options configure a Runner.
type Options struct {
	ServerURL string
	// AgentServerURL is the server's address as the containers reach it (through the proxy). Default: ServerURL.
	AgentServerURL string
	HTTP           *http.Client
	Tokens         rpc.Tokens
	Logger         *slog.Logger
	Name           string
	Driver         Driver
	// CacheDir holds the caches. It is a path on the engine's machine: a runner in a container mounts it at the same path.
	CacheDir string
	// Network is the internal network of the containers (default harness-cloud).
	Network string
	// SelfContainer is the runner's own container, when it runs in one: it joins the network as ProxyAlias.
	SelfContainer string
	// ProxyListen and ProxyURL override where the proxy listens and what the containers are told. Defaults: with
	// SelfContainer, :3128 and http://harness-egress:3128; otherwise the network's gateway address.
	ProxyListen, ProxyURL string
	// PollSeconds, MeasureEvery and PruneEvery set the pace (25 s, 5 min, 10 min); StopTimeout how long a container gets to
	// end (30 s). Tests shorten them.
	PollSeconds              int32
	MeasureEvery, PruneEvery time.Duration
	StopTimeout              time.Duration
	// MaxParallelStarts bounds containers being created at once (default 4).
	MaxParallelStarts int
}

// Runner keeps the containers the server wants.
type Runner struct {
	o   Options
	api relayv1connect.HostAgentServiceClient
	log *slog.Logger

	mu          sync.Mutex
	hostID      string
	config      *relayv1.CloudRunnerConfig
	creating    map[string]string // cloud session -> cache key
	removing    map[string]bool
	egress      map[string]sandbox.Hosts
	byIP        map[string]string
	running     map[string]bool // cache keys with a running container
	serverEntry string
	proxyURL    string
	proxyAddr   string
	lastMeasure time.Time
	lastPrune   time.Time
	starts      chan struct{}
	wg          sync.WaitGroup
}

// New makes a Runner; Run starts it.
func New(o Options) *Runner {
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	if o.Network == "" {
		o.Network = DefaultNetwork
	}
	if o.AgentServerURL == "" {
		o.AgentServerURL = o.ServerURL
	}
	if o.PollSeconds <= 0 {
		o.PollSeconds = 25
	}
	if o.MeasureEvery <= 0 {
		o.MeasureEvery = 5 * time.Minute
	}
	if o.PruneEvery <= 0 {
		o.PruneEvery = 10 * time.Minute
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 30 * time.Second
	}
	if o.MaxParallelStarts <= 0 {
		o.MaxParallelStarts = 4
	}
	if o.HTTP == nil {
		o.HTTP = rpc.HTTPClient(o.ServerURL)
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	r := &Runner{
		o: o, log: log.With("component", "runner"),
		api:      relayv1connect.NewHostAgentServiceClient(o.HTTP, strings.TrimRight(o.ServerURL, "/"), rpc.ClientOptions()...),
		creating: map[string]string{}, removing: map[string]bool{}, egress: map[string]sandbox.Hosts{}, byIP: map[string]string{}, running: map[string]bool{},
		starts: make(chan struct{}, o.MaxParallelStarts),
	}
	r.serverEntry = serverEntry(o.AgentServerURL)
	return r
}

// serverEntry is the server's host and port as an allowlist entry.
func serverEntry(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

func call[Req, Res any](ctx context.Context, tokens rpc.Tokens, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, error) {
	do := func(token string) (*Res, error) {
		req := connect.NewRequest(msg)
		rpc.SetBearer(req.Header(), token)
		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if tokens == nil {
		return do("")
	}
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	out, err := do(token)
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, rerr := tokens.Refresh(ctx); rerr == nil {
			out, err = do(token)
		}
	}
	return out, err
}

func backoff(n int) time.Duration {
	d := 500 * time.Millisecond << min(n, 6)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// HostID is the runner's host id ("" until registered).
func (r *Runner) HostID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hostID
}

// ProxyURL is what the containers are told to use ("" until Run set it up).
func (r *Runner) ProxyURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proxyURL
}

// ProxyAddr is where the egress proxy listens ("" until Run set it up).
func (r *Runner) ProxyAddr() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proxyAddr
}

func (r *Runner) register(ctx context.Context) (*relayv1.RegisterAgentHostResponse, error) {
	for attempt := 0; ; attempt++ {
		res, err := call(ctx, r.o.Tokens, r.api.RegisterAgentHost, &relayv1.RegisterAgentHostRequest{
			Name: r.o.Name, Isolation: relayv1.AgentHostIsolation_AGENT_HOST_ISOLATION_CONTAINER, SandboxDriver: "container",
			Os: runtime.GOOS, Arch: runtime.GOARCH, ClientVersion: config.Version, CpuCount: int32(runtime.NumCPU()),
		})
		if err == nil {
			return res, nil
		}
		switch connect.CodeOf(err) {
		case connect.CodeFailedPrecondition, connect.CodePermissionDenied, connect.CodeNotFound:
			return nil, err
		}
		r.log.Warn("registering with the server failed; retrying", "err", err)
		if !sleep(ctx, backoff(attempt)) {
			return nil, ctx.Err()
		}
	}
}

// Run registers, sets up the network and the proxy, and keeps the containers until ctx ends. Containers stay when it
// stops: the next run finds them by label, and the server says which to keep.
func (r *Runner) Run(ctx context.Context) error {
	if r.o.Driver == nil {
		return errors.New("no container engine")
	}
	if err := r.o.Driver.Ping(ctx); err != nil {
		return fmt.Errorf("the container engine does not answer: %w", err)
	}
	if r.o.CacheDir == "" || !filepath.IsAbs(r.o.CacheDir) {
		return errors.New("the cache directory must be an absolute path")
	}
	if err := os.MkdirAll(r.o.CacheDir, 0o711); err != nil {
		return fmt.Errorf("the cache directory: %w", err)
	}
	reg, err := r.register(ctx)
	if err != nil {
		return err
	}
	if !reg.GetRunner() {
		return errors.New("this token is not a runner's: an admin adds a runner in the settings (Hosts, Cloud runners) and gives you its token")
	}
	r.mu.Lock()
	r.hostID = reg.GetHost().GetId()
	r.mu.Unlock()
	netw, err := r.o.Driver.EnsureNetwork(ctx, r.o.Network, true, map[string]string{LabelRunner: r.hostID})
	if err != nil {
		return fmt.Errorf("the containers' network: %w", err)
	}
	ln, proxyURL, err := r.listenProxy(ctx, netw)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.proxyURL, r.proxyAddr = proxyURL, ln.Addr().String()
	r.mu.Unlock()
	srv := &http.Server{Handler: sandbox.NewEgressHandler(r.hostsFor), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	r.log.Info("registered", "runner", r.hostID, "name", reg.GetHost().GetName(), "network", netw.Name, "proxy", proxyURL)
	failures := 0
	for ctx.Err() == nil {
		if err := r.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				break
			}
			failures++
			r.log.Warn("polling the server failed", "err", err)
			sleep(ctx, backoff(failures))
			continue
		}
		failures = 0
	}
	r.wg.Wait()
	return ctx.Err()
}

// listenProxy opens the proxy where the containers can reach it.
func (r *Runner) listenProxy(ctx context.Context, netw Network) (net.Listener, string, error) {
	addr, u := r.o.ProxyListen, r.o.ProxyURL
	port := strconv.Itoa(DefaultProxyPort)
	if r.o.SelfContainer != "" {
		if err := r.o.Driver.ConnectNetwork(ctx, netw.ID, r.o.SelfContainer, []string{ProxyAlias}); err != nil {
			return nil, "", fmt.Errorf("joining the containers' network: %w", err)
		}
		if addr == "" {
			addr = ":" + port
		}
		if u == "" {
			u = "http://" + ProxyAlias + ":" + port
		}
	} else {
		if netw.Gateway == "" && (addr == "" || u == "") {
			return nil, "", errors.New("the containers' network has no gateway address to serve the proxy on; set --proxy-listen and --proxy-url")
		}
		if addr == "" {
			addr = net.JoinHostPort(netw.Gateway, port)
		}
		if u == "" {
			u = "http://" + net.JoinHostPort(netw.Gateway, port)
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("the egress proxy: %w", err)
	}
	return ln, u, nil
}

// hostsFor is the allowlist of the container that asks, by its address; nobody else may use the proxy.
func (r *Runner) hostsFor(req *http.Request) (sandbox.Hosts, bool) {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return sandbox.Hosts{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byIP[host]
	if !ok {
		return sandbox.Hosts{}, false
	}
	h, ok := r.egress[id]
	if !ok {
		return sandbox.Hosts{}, false
	}
	return h.Trusting(r.serverEntry), true
}

func (r *Runner) pollOnce(ctx context.Context) error {
	r.mu.Lock()
	measure := time.Since(r.lastMeasure) >= r.o.MeasureEvery
	hostID := r.hostID
	r.mu.Unlock()
	boxes, err := r.o.Driver.List(ctx, map[string]string{LabelRunner: hostID}, measure)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}
	req := &relayv1.PollCloudRunnerRequest{WaitSeconds: r.o.PollSeconds}
	byIP := map[string]string{}
	running := map[string]bool{}
	r.mu.Lock()
	if measure {
		r.lastMeasure = time.Now()
	}
	for _, b := range boxes {
		id := b.Labels[LabelSession]
		if r.removing[b.ID] {
			continue
		}
		c := &relayv1.CloudContainer{CloudSessionId: id, ContainerId: b.ID, Running: b.Running, CreatedAt: b.Created.UTC().Format(time.RFC3339)}
		if b.SizeRw >= 0 {
			c.DiskBytes = b.SizeRw
		}
		req.Containers = append(req.Containers, c)
		if ip := b.IPs[r.o.Network]; ip != "" && id != "" {
			byIP[ip] = id
		}
		if b.Running {
			running[b.Labels[LabelCacheKey]] = true
		}
	}
	for id := range r.creating {
		req.Creating = append(req.Creating, id)
	}
	r.byIP, r.running = byIP, running
	r.mu.Unlock()
	res, err := call(ctx, r.o.Tokens, r.api.PollCloudRunner, req)
	if err != nil {
		return err
	}
	r.apply(ctx, res)
	r.maybePrune()
	return nil
}

func (r *Runner) apply(ctx context.Context, res *relayv1.PollCloudRunnerResponse) {
	r.mu.Lock()
	r.config = res.GetConfig()
	egress := map[string]sandbox.Hosts{}
	for _, e := range res.GetEgress() {
		egress[e.GetCloudSessionId()] = sandbox.NewHosts(e.GetAllow())
	}
	r.egress = egress
	r.mu.Unlock()
	for _, rm := range res.GetRemove() {
		r.mu.Lock()
		busy := r.removing[rm.GetContainerId()]
		if !busy {
			r.removing[rm.GetContainerId()] = true
		}
		r.mu.Unlock()
		if busy {
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.remove(context.WithoutCancel(ctx), rm.GetCloudSessionId(), rm.GetContainerId(), rm.GetReason())
		}()
	}
	for _, spec := range res.GetStart() {
		r.mu.Lock()
		_, busy := r.creating[spec.GetCloudSessionId()]
		if !busy {
			r.creating[spec.GetCloudSessionId()] = spec.GetCacheKey()
		}
		r.mu.Unlock()
		if busy {
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() {
				r.mu.Lock()
				delete(r.creating, spec.GetCloudSessionId())
				r.mu.Unlock()
			}()
			select {
			case r.starts <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-r.starts }()
			r.start(ctx, spec)
		}()
	}
}

func (r *Runner) remove(ctx context.Context, sessionID, containerID, reason string) {
	defer func() {
		r.mu.Lock()
		delete(r.removing, containerID)
		r.mu.Unlock()
	}()
	r.log.Info("removing a container", "session", sessionID, "container", short(containerID), "reason", reason)
	if err := r.o.Driver.Stop(ctx, containerID, r.o.StopTimeout); err != nil {
		r.log.Debug("stopping a container failed; removing it anyway", "container", short(containerID), "err", err)
	}
	if err := r.o.Driver.Remove(ctx, containerID); err != nil {
		r.log.Warn("removing a container failed", "container", short(containerID), "err", err)
		return
	}
	if sessionID == "" {
		return
	}
	if _, err := call(ctx, r.o.Tokens, r.api.UpdateCloudSession, &relayv1.UpdateCloudSessionRequest{
		CloudSessionId: sessionID, State: agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_REMOVED, ContainerId: containerID,
	}); err != nil {
		r.log.Debug("reporting a removal failed", "session", sessionID, "err", err)
	}
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// containerEnv is the environment of a session's container: where the server is, the proxy, the caches. No secret.
func (r *Runner) containerEnv() []string {
	proxy := r.ProxyURL()
	env := []string{
		"HARNESS_SERVER_URL=" + r.o.AgentServerURL,
		"HARNESS_DEVICE_TOKEN_FILE=" + TokenPath,
		"HARNESS_KEYRING=0",
		"HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy, "https_proxy=" + proxy, "http_proxy=" + proxy,
		"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1",
	}
	return append(env, cacheEnv...)
}

func (r *Runner) start(ctx context.Context, spec *relayv1.CloudContainerSpec) {
	id := spec.GetCloudSessionId()
	fail := func(containerID string, err error) {
		r.log.Warn("starting a container failed", "session", id, "err", err)
		if containerID != "" {
			_ = r.o.Driver.Remove(context.WithoutCancel(ctx), containerID)
		}
		if _, uerr := call(ctx, r.o.Tokens, r.api.UpdateCloudSession, &relayv1.UpdateCloudSessionRequest{
			CloudSessionId: id, State: agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_FAILED, Error: err.Error(),
		}); uerr != nil {
			r.log.Debug("reporting a failed start failed", "session", id, "err", uerr)
		}
	}
	if !validCacheKey(spec.GetCacheKey()) {
		fail("", fmt.Errorf("the server named a cache %q this runner can't use", spec.GetCacheKey()))
		return
	}
	mounts, err := ensureCache(r.o.CacheDir, spec.GetCacheKey(), AgentUID, AgentUID)
	if err != nil {
		fail("", fmt.Errorf("the caches: %w", err))
		return
	}
	tok, err := call(ctx, r.o.Tokens, r.api.IssueCloudSessionToken, &relayv1.IssueCloudSessionTokenRequest{CloudSessionId: id})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeNotFound {
			r.log.Warn("no token for a container", "session", id, "err", err)
		}
		return
	}
	r.mu.Lock()
	cfg := r.config
	hostID := r.hostID
	r.mu.Unlock()
	cs := ContainerSpec{
		Name:        "harness-" + id,
		Image:       spec.GetImage(),
		Labels:      map[string]string{LabelRunner: hostID, LabelSession: id, LabelCacheKey: spec.GetCacheKey()},
		Env:         r.containerEnv(),
		User:        fmt.Sprintf("%d:%d", AgentUID, AgentUID),
		Runtime:     cfg.GetRuntime(),
		CPUMillis:   int(cfg.GetCpuMillis()),
		MemoryBytes: cfg.GetMemoryBytes(),
		Pids:        int64(cfg.GetPids()),
		Network:     r.o.Network,
		Mounts:      mounts,
	}
	if cfg.GetStorageOptSize() {
		cs.DiskBytes = cfg.GetDiskBytes()
	}
	containerID, err := r.create(ctx, cs)
	if err != nil {
		fail("", err)
		return
	}
	file := File{Path: TokenPath, Mode: 0o600, UID: AgentUID, GID: AgentUID, Data: []byte(tok.GetDeviceToken())}
	if err := r.o.Driver.PutFiles(ctx, containerID, []File{file}); err != nil {
		fail(containerID, fmt.Errorf("copying the token in: %w", err))
		return
	}
	if err := r.o.Driver.Start(ctx, containerID); err != nil {
		fail(containerID, err)
		return
	}
	res, err := call(ctx, r.o.Tokens, r.api.UpdateCloudSession, &relayv1.UpdateCloudSessionRequest{
		CloudSessionId: id, State: agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_STARTING, ContainerId: containerID,
	})
	if err == nil && res.GetRemove() {
		// Cancelled while it was being made.
		r.remove(context.WithoutCancel(ctx), id, containerID, "no longer wanted")
		return
	}
	r.log.Info("started a container", "session", id, "container", short(containerID), "image", cs.Image)
}

// create makes the container, pulling its image when the engine lacks it and clearing a stale one of the same name.
func (r *Runner) create(ctx context.Context, cs ContainerSpec) (string, error) {
	for attempt := 0; ; attempt++ {
		id, err := r.o.Driver.Create(ctx, cs)
		if err == nil {
			return id, nil
		}
		if attempt > 0 {
			return "", err
		}
		var ae *apiError
		switch {
		case errors.Is(err, ErrNoSuchImage):
			r.log.Info("pulling an image", "image", cs.Image)
			if perr := r.o.Driver.Pull(ctx, cs.Image); perr != nil {
				return "", fmt.Errorf("the image %s: %w", cs.Image, perr)
			}
		case errors.As(err, &ae) && ae.Status == http.StatusConflict:
			// A container of an earlier try with this name: it is no use any more.
			if rerr := r.o.Driver.Remove(ctx, cs.Name); rerr != nil {
				return "", err
			}
		default:
			return "", err
		}
	}
}

// maybePrune keeps the caches of keys with no running container under the cap.
func (r *Runner) maybePrune() {
	r.mu.Lock()
	if time.Since(r.lastPrune) < r.o.PruneEvery || r.config == nil {
		r.mu.Unlock()
		return
	}
	r.lastPrune = time.Now()
	max := r.config.GetCacheMaxBytes()
	busy := map[string]bool{}
	for k := range r.running {
		busy[k] = true
	}
	for _, key := range r.creating {
		busy[key] = true
	}
	r.mu.Unlock()
	entries, err := os.ReadDir(r.o.CacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !validCacheKey(e.Name()) || busy[e.Name()] {
			continue
		}
		size, err := pruneCache(filepath.Join(r.o.CacheDir, e.Name()), max)
		if err != nil {
			r.log.Debug("pruning a cache failed", "cache", e.Name(), "err", err)
			continue
		}
		r.log.Debug("cache", "key", e.Name(), "bytes", size)
	}
}
