package sshhost

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/ssh"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// A fake host: an SSH server in this process that runs commands in a temporary home, with throwaway keys. The client side is
// the real ssh command, with a config of its own, so the developer's ~/.ssh takes no part. No real host or key is ever used.

type fakeHost struct {
	t        *testing.T
	ln       net.Listener
	home     string
	signer   ssh.Signer
	env      []string
	mu       sync.Mutex
	commands []string
	authed   []string
}

func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startFakeHost(t *testing.T, signer ssh.Signer, port int) *fakeHost {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{t: t, ln: ln, home: t.TempDir(), signer: signer}
	t.Cleanup(func() { _ = ln.Close() })
	go h.serve()
	return h
}

func (h *fakeHost) port() int { return h.ln.Addr().(*net.TCPAddr).Port }

func (h *fakeHost) ran() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.commands...)
}

func (h *fakeHost) serve() {
	cfg := &ssh.ServerConfig{
		// "none" is refused: a client that offers nothing gets "Permission denied", as from a real server.
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			h.mu.Lock()
			h.authed = append(h.authed, meta.User())
			h.mu.Unlock()
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(h.signer)
	for {
		c, err := h.ln.Accept()
		if err != nil {
			return
		}
		go h.handle(c, cfg)
	}
}

func (h *fakeHost) handle(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, chreqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go h.session(ch, chreqs)
		case "direct-tcpip":
			var d struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}
			if err := ssh.Unmarshal(nc.ExtraData(), &d); err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, "bad request")
				continue
			}
			target, err := net.Dial("tcp", net.JoinHostPort(d.Host, strconv.Itoa(int(d.Port))))
			if err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, r, err := nc.Accept()
			if err != nil {
				target.Close()
				continue
			}
			go ssh.DiscardRequests(r)
			go func() {
				_, _ = io.Copy(target, ch)
				target.Close()
			}()
			go func() {
				_, _ = io.Copy(ch, target)
				_ = ch.CloseWrite()
				ch.Close()
			}()
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func (h *fakeHost) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var p struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &p); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)
		h.mu.Lock()
		h.commands = append(h.commands, p.Command)
		h.mu.Unlock()
		cmd := exec.Command("/bin/sh", "-c", p.Command)
		cmd.Dir = h.home
		cmd.Env = append([]string{"HOME=" + h.home, "PATH=" + os.Getenv("PATH"), "HARNESS_HOME=" + filepath.Join(h.home, ".harness"), "HARNESS_KEYRING=0"}, h.env...)
		cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
		in, err := cmd.StdinPipe()
		if err != nil {
			return
		}
		go func() {
			_, _ = io.Copy(in, ch)
			_ = in.Close()
		}()
		code := 0
		if err := cmd.Run(); err != nil {
			code = 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		st := make([]byte, 4)
		binary.BigEndian.PutUint32(st, uint32(code))
		_, _ = ch.SendRequest("exit-status", false, st)
		return
	}
}

type rig struct {
	t      *testing.T
	client *Client
	store  *Store
	host   Host
	fake   *fakeHost
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh command")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen command")
	}
	fake := startFakeHost(t, newHostKey(t), 0)
	// A throwaway client key, named for this test.
	dir, err := os.MkdirTemp("", "hssh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "id")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(config, []byte("Host *\n  IdentityFile "+keyFile+"\n  IdentitiesOnly yes\n  LogLevel ERROR\n  User tester\n  CheckHostIP no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Remembered keys only in the test's folder: the home of the known_hosts path in the command line is the developer's, so it
	// must not know 127.0.0.1 on this port. A random high port makes that a safe bet.
	client := &Client{Dir: filepath.Join(dir, "ssh"), ExtraArgs: []string{"-F", config, "-o", "GlobalKnownHostsFile=/dev/null"}, ConnectTimeout: 5 * time.Second}
	store := NewStore(filepath.Join(dir, "ssh", "hosts.json"))
	host, err := store.Save(Host{Name: "fake", Destination: "127.0.0.1", Port: fake.port()})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, client: client, store: store, host: host, fake: fake}
}

func (r *rig) trust() {
	r.t.Helper()
	ctx := context.Background()
	check, err := r.client.CheckKey(ctx, r.host)
	if err != nil || check.Status != KeyUnknown {
		r.t.Fatalf("first check: %+v %v", check, err)
	}
	if err := r.client.TrustKey(ctx, r.host, check.Fingerprint); err != nil {
		r.t.Fatal(err)
	}
}

func TestStoreAndValidation(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "ssh", "hosts.json"))
	h, err := s.Save(Host{Destination: "me@example.com", Port: 2222})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.ID, "ssh_") {
		t.Errorf("id %q", h.ID)
	}
	h.Name = "box"
	if _, err := s.Save(h); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil || len(all) != 1 || all[0].Name != "box" {
		t.Fatalf("%v %v", all, err)
	}
	if _, err := s.Save(Host{ID: "ssh_gone", Destination: "x"}); !errors.Is(err, ErrNoHost) {
		t.Errorf("update of nothing: %v", err)
	}
	if err := s.Delete(h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(h.ID); !errors.Is(err, ErrNoHost) {
		t.Errorf("get: %v", err)
	}
	if st, err := os.Stat(s.path); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v %v", st, err)
	}
	for _, bad := range []string{"", "-oProxyCommand=touch /tmp/pwned", "a b", "host;rm", "$(id)", "host\nname", "`id`"} {
		if err := (Host{Destination: bad}).Validate(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"box", "me@example.com", "user@[::1]", "a.b-c_d"} {
		if err := (Host{Destination: good}).Validate(); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

func TestSshHelpers(t *testing.T) {
	if got := shellQuote(`a'b c`); got != `'a'\''b c'` {
		t.Errorf("%q", got)
	}
	if u, args, err := serverForward("https://harness.example.com/"); err != nil || u == "" || args != nil {
		t.Errorf("%q %v %v", u, args, err)
	}
	if _, args, err := serverForward("http://127.0.0.1:3737"); err != nil || strings.Join(args[:2], " ") != "-R 127.0.0.1:3737:127.0.0.1:3737" {
		t.Errorf("%v %v", args, err)
	}
	c := &Client{Dir: "/home/me/Application Support/ssh"}
	line := strings.Join(c.args(Host{Destination: "box"}), "\x00")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", `"/home/me/Application Support/ssh/known_hosts"`, "--\x00box"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q missing from %q", want, line)
		}
	}
}

func TestHostKeyIsVerifiedBeforeAnythingGoesThrough(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	// Unknown: ssh refuses to run anything, and no credentials were offered.
	if _, err := r.client.Run(ctx, r.host, nil, "touch ran"); err == nil {
		t.Fatal("ran on an unverified host")
	}
	if _, err := os.Stat(filepath.Join(r.fake.home, "ran")); err == nil {
		t.Fatal("the command got through")
	}
	check, err := r.client.CheckKey(ctx, r.host)
	if err != nil || check.Status != KeyUnknown {
		t.Fatalf("%+v %v", check, err)
	}
	r.fake.mu.Lock()
	offered := len(r.fake.authed)
	r.fake.mu.Unlock()
	if offered != 0 {
		t.Errorf("credentials reached an unverified host %d times", offered)
	}
	want := ssh.FingerprintSHA256(r.fake.signer.PublicKey())
	if !strings.Contains(check.Fingerprint, want) {
		t.Errorf("fingerprint %q, want %q", check.Fingerprint, want)
	}

	// Another fingerprint than the one shown is not trusted.
	if err := r.client.TrustKey(ctx, r.host, "SHA256:nonsense (ED25519)"); !errors.Is(err, ErrKeyMoved) {
		t.Fatalf("%v", err)
	}
	if err := r.client.TrustKey(ctx, r.host, check.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if c, err := r.client.CheckKey(ctx, r.host); err != nil || c.Status != KeyVerified {
		t.Fatalf("after trust: %+v %v", c, err)
	}
	if out, err := r.client.Run(ctx, r.host, nil, "echo hello"); err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("%q %v", out, err)
	}

	// The key changes: everything stops, and it is only accepted after forgetting the old one on purpose.
	port := r.fake.port()
	_ = r.fake.ln.Close()
	var other *fakeHost
	for i := 0; ; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			_ = ln.Close()
			other = startFakeHost(t, newHostKey(t), port)
			break
		}
		if i > 200 {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, err := r.client.CheckKey(ctx, r.host); err != nil || c.Status != KeyChanged {
		t.Fatalf("changed: %+v %v", c, err)
	}
	if _, err := r.client.Run(ctx, r.host, nil, "touch ran"); err == nil {
		t.Fatal("ran on a host whose key changed")
	}
	if _, err := os.Stat(filepath.Join(other.home, "ran")); err == nil {
		t.Fatal("the command got through")
	}
	m := NewManager(Options{Client: r.client, Store: r.store, Version: "x"})
	if _, err := m.Connect(ctx, r.host.ID, "", func(Stage) {}); !errors.Is(err, ErrKeyNotVerified) {
		t.Fatalf("connect: %v", err)
	}
	if err := r.client.ForgetKey(ctx, r.host); err != nil {
		t.Fatal(err)
	}
	if c, err := r.client.CheckKey(ctx, r.host); err != nil || c.Status != KeyUnknown {
		t.Fatalf("after forgetting: %+v %v", c, err)
	}
}

func TestUnreachableHostSaysSo(t *testing.T) {
	r := newRig(t)
	_ = r.fake.ln.Close()
	c, err := r.client.CheckKey(context.Background(), r.host)
	if err != nil || c.Status != KeyUnreachable || c.Err == "" {
		t.Fatalf("%+v %v", c, err)
	}
}

// buildClient builds the real harness-client once for the tests here. Go's build cache keeps this cheap.
var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

func builtClient(t *testing.T, version string) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "harness-client-build")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "harness-client")
		cmd := exec.Command("go", "build", "-ldflags", "-X github.com/xlsft/harness/packages/client/config.Version="+version, "-o", buildPath, "github.com/xlsft/harness/packages/client/cmd/harness-client")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return buildPath
}

func TestConnectDeploysTunnelsAndTheSameRpcsWork(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the client")
	}
	r := newRig(t)
	r.trust()
	const version = "9.9.9-test"
	bin := builtClient(t, version)

	gw := fg.New(fg.Sequence(func(req *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text("hello from the host"))}
	}))
	url, stop, err := gw.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	var binaryAsked []string
	var mu sync.Mutex
	said, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{
		Client: r.client, Store: r.store, Version: strings.TrimSpace(string(said)), ServerURL: func() string { return url }, ServerReachable: true,
		Binaries: func(_ context.Context, goos, goarch string) (string, func(), error) {
			mu.Lock()
			binaryAsked = append(binaryAsked, goos+"/"+goarch)
			mu.Unlock()
			return bin, nil, nil
		},
	})
	t.Cleanup(m.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stages []Stage
	conn, err := m.Connect(ctx, r.host.ID, "http://localhost:3210", func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("connect: %v (stages %+v, host ran %v)", err, stages, r.fake.ran())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; conn.Platform != want || len(binaryAsked) != 1 || binaryAsked[0] != want {
		t.Errorf("platform %q, asked %v", conn.Platform, binaryAsked)
	}
	names := []string{}
	for _, s := range stages {
		names = append(names, s.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"key", "detect", "deploy", "sign_in", "start", "tunnel", "ready"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no stage %q in %s", want, joined)
		}
	}
	var code Stage
	for _, s := range stages {
		if s.UserCode != "" {
			code = s
		}
	}
	if code.UserCode != "TEST-CODE" || code.VerificationURI == "" {
		t.Errorf("the code to confirm: %+v", code)
	}

	// The upload landed where it should, and no secret was on a command line.
	if st, err := os.Stat(filepath.Join(r.fake.home, ".harness", "bin", "harness-client")); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("uploaded: %v %v", st, err)
	}
	for _, c := range r.fake.ran() {
		if strings.Contains(c, conn.Token) {
			t.Errorf("the token is on a command line: %s", c)
		}
	}

	// The same client an interface would use, through the tunnel: a session runs on the host and its model answers.
	hc := agentv1connect.NewAgentServiceClient(httpClient(conn.Token), conn.URL)
	if _, err := hc.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{})); err != nil {
		t.Fatalf("initialize through the tunnel: %v", err)
	}
	cs, err := hc.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: r.fake.home}))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if cs.Msg.Session.GetCwd() != r.fake.home && !strings.HasSuffix(cs.Msg.Session.GetCwd(), filepath.Base(r.fake.home)) {
		t.Errorf("session runs in %q", cs.Msg.Session.GetCwd())
	}
	stream, err := hc.Subscribe(ctx, connect.NewRequest(&agentv1.SubscribeRequest{SessionId: cs.Msg.Session.Id}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hc.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: cs.Msg.Session.Id, Content: []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "hi"}}}}})); err != nil {
		t.Fatal(err)
	}
	answered := false
	for !answered && stream.Receive() {
		if m := stream.Msg().Event.GetAssistantMessage(); m != nil {
			answered = true
		}
		if stream.Msg().Event.GetTurnFinished() != nil {
			break
		}
	}
	stream.Close()
	if !answered {
		t.Errorf("no answer through the tunnel: %v", stream.Err())
	}

	// Without the token the tunnel gets nowhere.
	bare := agentv1connect.NewAgentServiceClient(httpClient("wrong"), conn.URL)
	if _, err := bare.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("wrong token: %v", err)
	}

	// Connecting again finds the client there and does not upload it again.
	if len(m.Connections()) != 1 {
		t.Fatalf("connections: %d", len(m.Connections()))
	}
	m.Disconnect(r.host.ID)
	if len(m.Connections()) != 0 {
		t.Error("still connected")
	}
	waitClosed(t, conn.URL)
	stages = nil
	again, err := m.Connect(ctx, r.host.ID, "", func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if len(binaryAsked) != 1 {
		t.Errorf("uploaded again: %v", binaryAsked)
	}
	if conn.Token == again.Token {
		t.Error("the token was used twice")
	}
	for _, s := range stages {
		if s.Name == "sign_in" {
			t.Error("signed in again")
		}
	}
}

func waitClosed(t *testing.T, url string) {
	t.Helper()
	addr := strings.TrimPrefix(url, "http://")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the tunnel stayed open after disconnecting")
}
