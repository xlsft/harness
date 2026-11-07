package cloudrunner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
)

// fakeDriver is an engine in memory.
type fakeDriver struct {
	mu       sync.Mutex
	boxes    map[string]*Container
	specs    map[string]ContainerSpec
	files    map[string][]File
	removed  []string
	pulled   []string
	noImage  bool
	ip       string
	next     int
	joined   []string
	creating chan struct{}
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{boxes: map[string]*Container{}, specs: map[string]ContainerSpec{}, files: map[string][]File{}, ip: "127.0.0.1"}
}

func (d *fakeDriver) Ping(context.Context) error { return nil }
func (d *fakeDriver) EnsureNetwork(_ context.Context, name string, internal bool, _ map[string]string) (Network, error) {
	return Network{ID: "net1", Name: name, Internal: internal, Gateway: "127.0.0.1"}, nil
}
func (d *fakeDriver) ConnectNetwork(_ context.Context, _, c string, _ []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.joined = append(d.joined, c)
	return nil
}
func (d *fakeDriver) Pull(_ context.Context, image string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pulled = append(d.pulled, image)
	d.noImage = false
	return nil
}
func (d *fakeDriver) Create(_ context.Context, s ContainerSpec) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.noImage {
		return "", fmt.Errorf("%w: %s", ErrNoSuchImage, s.Image)
	}
	d.next++
	id := fmt.Sprintf("c%d", d.next)
	d.specs[id] = s
	d.boxes[id] = &Container{ID: id, Name: s.Name, Labels: s.Labels, Created: time.Now(), IPs: map[string]string{s.Network: d.ip}, SizeRw: -1}
	return id, nil
}
func (d *fakeDriver) PutFiles(_ context.Context, id string, files []File) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[id] = append(d.files[id], files...)
	return nil
}
func (d *fakeDriver) Start(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.boxes[id].Running = true
	return nil
}
func (d *fakeDriver) Stop(_ context.Context, id string, _ time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if b := d.boxes[id]; b != nil {
		b.Running = false
	}
	return nil
}
func (d *fakeDriver) Remove(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.boxes, id)
	d.removed = append(d.removed, id)
	return nil
}
func (d *fakeDriver) List(_ context.Context, labels map[string]string, sizes bool) ([]Container, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Container
	for _, b := range d.boxes {
		match := true
		for k, v := range labels {
			match = match && b.Labels[k] == v
		}
		if match {
			c := *b
			if sizes {
				c.SizeRw = 1234
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// fakeServer is the server's side of a runner: scripted poll answers, and what the runner reported.
type fakeServer struct {
	relayv1connect.UnimplementedHostAgentServiceHandler
	mu      sync.Mutex
	answers []*relayv1.PollCloudRunnerResponse
	polls   []*relayv1.PollCloudRunnerRequest
	updates []*relayv1.UpdateCloudSessionRequest
	tokens  int
}

func (s *fakeServer) RegisterAgentHost(context.Context, *connect.Request[relayv1.RegisterAgentHostRequest]) (*connect.Response[relayv1.RegisterAgentHostResponse], error) {
	return connect.NewResponse(&relayv1.RegisterAgentHostResponse{Host: &relayv1.AgentHost{Id: "ahs_r", Name: "r"}, Runner: true}), nil
}

func (s *fakeServer) PollCloudRunner(ctx context.Context, req *connect.Request[relayv1.PollCloudRunnerRequest]) (*connect.Response[relayv1.PollCloudRunnerResponse], error) {
	s.mu.Lock()
	s.polls = append(s.polls, req.Msg)
	var next *relayv1.PollCloudRunnerResponse
	if len(s.answers) > 0 {
		next, s.answers = s.answers[0], s.answers[1:]
	}
	s.mu.Unlock()
	if next == nil {
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
		next = &relayv1.PollCloudRunnerResponse{Config: &relayv1.CloudRunnerConfig{Runtime: "runsc", CpuMillis: 1000, MemoryBytes: 1 << 30, Pids: 256, CacheMaxBytes: 1 << 20}}
	}
	return connect.NewResponse(next), nil
}

func (s *fakeServer) IssueCloudSessionToken(_ context.Context, req *connect.Request[relayv1.IssueCloudSessionTokenRequest]) (*connect.Response[relayv1.IssueCloudSessionTokenResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens++
	return connect.NewResponse(&relayv1.IssueCloudSessionTokenResponse{DeviceId: "dev_1", DeviceToken: fmt.Sprintf("hdt_secret_%d", s.tokens)}), nil
}

func (s *fakeServer) UpdateCloudSession(_ context.Context, req *connect.Request[relayv1.UpdateCloudSessionRequest]) (*connect.Response[relayv1.UpdateCloudSessionResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, req.Msg)
	return connect.NewResponse(&relayv1.UpdateCloudSessionResponse{}), nil
}

func (s *fakeServer) answer(res *relayv1.PollCloudRunnerResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if res.Config == nil {
		res.Config = &relayv1.CloudRunnerConfig{Runtime: "runsc", CpuMillis: 1000, MemoryBytes: 1 << 30, Pids: 256, CacheMaxBytes: 1 << 20}
	}
	s.answers = append(s.answers, res)
}

func (s *fakeServer) updated(state agentv1.CloudEnvironmentState) []*relayv1.UpdateCloudSessionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*relayv1.UpdateCloudSessionRequest
	for _, u := range s.updates {
		if u.GetState() == state {
			out = append(out, u)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startRunner(t *testing.T, d *fakeDriver, fs *fakeServer) (*Runner, context.CancelFunc) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(relayv1connect.NewHostAgentServiceHandler(fs))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r := New(Options{
		ServerURL: srv.URL, AgentServerURL: "https://harness.example.com", HTTP: srv.Client(), Driver: d, CacheDir: t.TempDir(),
		ProxyListen: "127.0.0.1:0", ProxyURL: "http://127.0.0.1:3128", PollSeconds: 1, PruneEvery: time.Hour, StopTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "the proxy", func() bool { return r.ProxyAddr() != "" })
	return r, cancel
}

func TestRunnerStartsAndRemovesContainers(t *testing.T) {
	d := newFakeDriver()
	d.noImage = true
	fs := &fakeServer{}
	fs.answer(&relayv1.PollCloudRunnerResponse{
		Start:  []*relayv1.CloudContainerSpec{{CloudSessionId: "cls_1", Image: "harness-agent:test", CacheKey: "u0123456789abcdef"}},
		Egress: []*relayv1.CloudEgress{{CloudSessionId: "cls_1", Allow: []string{"registry.npmjs.org"}}},
	})
	r, _ := startRunner(t, d, fs)
	waitFor(t, "the container to start", func() bool {
		return len(fs.updated(agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_STARTING)) == 1
	})
	d.mu.Lock()
	spec, files := d.specs["c1"], d.files["c1"]
	pulled := d.pulled
	running := d.boxes["c1"].Running
	d.mu.Unlock()
	if len(pulled) != 1 || pulled[0] != "harness-agent:test" {
		t.Errorf("the missing image is pulled: %v", pulled)
	}
	if !running {
		t.Error("the container runs")
	}
	if spec.Runtime != "runsc" || spec.CPUMillis != 1000 || spec.MemoryBytes != 1<<30 || spec.Pids != 256 || spec.User != "10001:10001" || spec.Network != DefaultNetwork {
		t.Errorf("spec %+v", spec)
	}
	if spec.DiskBytes != 0 {
		t.Error("no storage-opt size unless the runner's config says the engine supports it")
	}
	if spec.Labels[LabelRunner] != "ahs_r" || spec.Labels[LabelSession] != "cls_1" || spec.Labels[LabelCacheKey] != "u0123456789abcdef" {
		t.Errorf("labels %v", spec.Labels)
	}
	env := strings.Join(spec.Env, "\n")
	for _, want := range []string{"HARNESS_SERVER_URL=https://harness.example.com", "HTTPS_PROXY=http://127.0.0.1:3128", "HARNESS_DEVICE_TOKEN_FILE=/run/harness/token", "GOMODCACHE=/cache/go/mod", "UV_CACHE_DIR=/cache/uv"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "hdt_") {
		t.Fatal("the token must never be in the environment")
	}
	if len(files) != 1 || files[0].Path != TokenPath || string(files[0].Data) != "hdt_secret_1" || files[0].Mode != 0o600 || files[0].UID != AgentUID {
		t.Fatalf("token file %+v", files)
	}
	if len(spec.Mounts) != 3 || spec.Mounts[0].Target != "/cache/pnpm" || !strings.HasSuffix(spec.Mounts[0].Source, "u0123456789abcdef/pnpm") {
		t.Errorf("cache mounts %+v", spec.Mounts)
	}
	if fi, err := os.Stat(spec.Mounts[1].Source); err != nil || !fi.IsDir() {
		t.Errorf("cache dir: %v", err)
	}

	// The next poll reports it, and the server says it is idle: it goes.
	waitFor(t, "a poll with the container", func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		last := fs.polls[len(fs.polls)-1]
		return len(last.GetContainers()) == 1 && last.GetContainers()[0].GetCloudSessionId() == "cls_1" && last.GetContainers()[0].GetRunning()
	})
	_ = r
	fs.answer(&relayv1.PollCloudRunnerResponse{Remove: []*relayv1.CloudContainerRemoval{{CloudSessionId: "cls_1", ContainerId: "c1", Reason: "idle"}}})
	waitFor(t, "the removal", func() bool {
		return len(fs.updated(agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_REMOVED)) == 1
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.removed) != 1 || d.removed[0] != "c1" || len(d.boxes) != 0 {
		t.Errorf("removed %v, left %v", d.removed, d.boxes)
	}
}

func TestRunnerReportsAFailedStart(t *testing.T) {
	d := newFakeDriver()
	fs := &fakeServer{}
	fs.answer(&relayv1.PollCloudRunnerResponse{Start: []*relayv1.CloudContainerSpec{{CloudSessionId: "cls_2", Image: "x", CacheKey: "../../etc"}}})
	startRunner(t, d, fs)
	waitFor(t, "the failure", func() bool { return len(fs.updated(agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_FAILED)) == 1 })
	if !strings.Contains(fs.updated(agentv1.CloudEnvironmentState_CLOUD_ENVIRONMENT_STATE_FAILED)[0].GetError(), "cache") {
		t.Error("a cache key that names a path is refused")
	}
	if fs.tokens != 0 {
		t.Error("no token for a container that won't be made")
	}
}

// proxyGet asks the proxy for a URL as a container would.
func proxyGet(t *testing.T, proxy, target string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, u.Host)
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestEgressProxyPerContainer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "hello") }))
	defer up.Close()
	upHost := strings.TrimPrefix(up.URL, "http://")
	d := newFakeDriver()
	fs := &fakeServer{}
	fs.answer(&relayv1.PollCloudRunnerResponse{
		Start:  []*relayv1.CloudContainerSpec{{CloudSessionId: "cls_3", Image: "x", CacheKey: "runner"}},
		Egress: []*relayv1.CloudEgress{{CloudSessionId: "cls_3", Allow: []string{upHost}}},
	})
	r, _ := startRunner(t, d, fs)
	// Before the runner knows the container's address, nobody may use the proxy.
	if code, _ := proxyGet(t, r.ProxyAddr(), up.URL); code != http.StatusForbidden {
		t.Errorf("unknown client: %d", code)
	}
	waitFor(t, "the container's address", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.byIP["127.0.0.1"] == "cls_3"
	})
	// The egress list comes with every poll; keep it.
	fs.mu.Lock()
	fs.answers = append(fs.answers, &relayv1.PollCloudRunnerResponse{Config: &relayv1.CloudRunnerConfig{}, Egress: []*relayv1.CloudEgress{{CloudSessionId: "cls_3", Allow: []string{upHost}}}})
	for range 50 {
		fs.answers = append(fs.answers, &relayv1.PollCloudRunnerResponse{Config: &relayv1.CloudRunnerConfig{}, Egress: []*relayv1.CloudEgress{{CloudSessionId: "cls_3", Allow: []string{upHost}}}})
	}
	fs.mu.Unlock()
	waitFor(t, "the allowlist", func() bool {
		code, body := proxyGet(t, r.ProxyAddr(), up.URL)
		return code == http.StatusOK && body == "hello"
	})
	if code, body := proxyGet(t, r.ProxyAddr(), "http://example.invalid/"); code != http.StatusForbidden || !strings.Contains(body, "not on the allowed hosts") {
		t.Errorf("a host off the list: %d %s", code, body)
	}
}

func TestPruneCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i, name := range []string{"old", "mid", "new"} {
		p := filepath.Join(dir, "go", "mod", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 1000), 0o444); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(i-3) * time.Hour)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "go", "mod"), 0o500); err != nil {
		t.Fatal(err)
	}
	size, err := pruneCache(dir, 2500)
	if err != nil {
		t.Fatal(err)
	}
	if size > 2500 {
		t.Errorf("still %d bytes", size)
	}
	if _, err := os.Stat(filepath.Join(dir, "go", "mod", "old")); !os.IsNotExist(err) {
		t.Error("the least recently used file goes first")
	}
	if _, err := os.Stat(filepath.Join(dir, "go", "mod", "new")); err != nil {
		t.Error("the newest stays")
	}
	if !validCacheKey("runner") || !validCacheKey("u0123456789abcdef") || validCacheKey("../x") || validCacheKey("u01") {
		t.Error("cache keys")
	}
}
