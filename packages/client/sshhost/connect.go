package sshhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RemoteBin is where an uploaded client lives on a host, under its home.
const RemoteBin = "$HOME/.harness/bin/harness-client"

// Binaries finds a harness-client for a platform of a host ("linux", "amd64"). done cleans up after it was uploaded.
type Binaries func(ctx context.Context, goos, goarch string) (path string, done func(), err error)

// OwnBinary offers this very program, for a host of the same platform.
func OwnBinary(_ context.Context, goos, goarch string) (string, func(), error) {
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		return "", nil, fmt.Errorf("this build carries no client for %s/%s", goos, goarch)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	return exe, nil, nil
}

// Options configure a Manager.
type Options struct {
	Client *Client
	Store  *Store
	// Binaries provides what to upload when the host has no matching client.
	Binaries Binaries
	// ServerURL is the harness server the host's client signs in to; Version what a host's client must say.
	ServerURL func() string
	Version   string
	// ServerReachable says the host reaches a server on this machine's loopback by itself (a test whose "host" is this
	// machine). Otherwise such a server goes through a reverse tunnel.
	ServerReachable bool
	// Ready bounds waiting for the client on the host to answer (0: 30 seconds).
	Ready time.Duration
}

// Connection is a live connection to a host's client.
type Connection struct {
	HostID   string
	URL      string
	Token    string
	Platform string
	Email    string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	done   chan struct{}
	stderr *bytes.Buffer
}

// Manager holds the connections of this client.
type Manager struct {
	o  Options
	mu sync.Mutex
	// By host id.
	conns map[string]*Connection
}

// NewManager makes one.
func NewManager(o Options) *Manager {
	return &Manager{o: o, conns: map[string]*Connection{}}
}

// Stage is a step of Connect, for people.
type Stage struct {
	Name, Message string
	// VerificationURI and UserCode: the user must confirm this code for the host's client to sign in.
	VerificationURI, UserCode string
}

// Connections returns what is connected now. One that dropped is gone from here.
func (m *Manager) Connections() []*Connection {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Connection, 0, len(m.conns))
	for _, c := range m.conns {
		select {
		case <-c.done:
		default:
			out = append(out, c)
		}
	}
	return out
}

// Disconnect ends a host's connection: the client there exits when its stdin closes.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	c := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// Close ends all connections.
func (m *Manager) Close() {
	m.mu.Lock()
	all := m.conns
	m.conns = map[string]*Connection{}
	m.mu.Unlock()
	for _, c := range all {
		c.close()
	}
}

func (c *Connection) close() {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	select {
	case <-c.done:
		return
	case <-time.After(3 * time.Second):
	}
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	<-c.done
}

// ErrKeyNotVerified stops a connection before anything reaches the host.
var ErrKeyNotVerified = errors.New("the host's key is not verified")

// serverForward says how the host reaches the server: directly, or, when the server is on this machine's loopback, through a
// reverse tunnel on the same port of the host's loopback.
func serverForward(raw string) (remoteURL string, args []string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", nil, fmt.Errorf("the harness server's URL %q cannot be used", raw)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return raw, nil, nil
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return raw, []string{"-R", fmt.Sprintf("127.0.0.1:%s:%s:%s", port, host, port), "-o", "ExitOnForwardFailure=yes"}, nil
}

// Connect connects to a saved host and reports each stage: check the key, detect the platform, find or upload the client, sign it
// in, start it, tunnel to it. Only one connection per host: another Connect replaces it.
func (m *Manager) Connect(ctx context.Context, id, origin string, report func(Stage)) (*Connection, error) {
	h, err := m.o.Store.Get(id)
	if err != nil {
		return nil, err
	}
	c := m.o.Client

	report(Stage{Name: "key", Message: "Checking " + h.Destination + "'s key"})
	check, err := c.CheckKey(ctx, h)
	if err != nil {
		return nil, err
	}
	switch check.Status {
	case KeyVerified:
	case KeyUnreachable:
		return nil, fmt.Errorf("could not reach %s: %s", h.Destination, check.Err)
	case KeyChanged:
		return nil, fmt.Errorf("%w: %s's key has changed", ErrKeyNotVerified, h.Destination)
	default:
		return nil, fmt.Errorf("%w: %s has not been seen before", ErrKeyNotVerified, h.Destination)
	}

	serverURL := ""
	if m.o.ServerURL != nil {
		serverURL = strings.TrimRight(m.o.ServerURL(), "/")
	}
	var forward []string
	if serverURL != "" && !m.o.ServerReachable {
		if _, forward, err = serverForward(serverURL); err != nil {
			return nil, err
		}
	}

	report(Stage{Name: "detect", Message: "Looking at the machine"})
	platform, err := m.detect(ctx, h)
	if err != nil {
		return nil, err
	}

	bin, err := m.findOrDeploy(ctx, h, platform, report)
	if err != nil {
		return nil, err
	}

	email := ""
	if serverURL != "" {
		if email, err = m.signIn(ctx, h, bin, serverURL, forward, report); err != nil {
			return nil, err
		}
	}

	report(Stage{Name: "start", Message: "Starting the client there"})
	conn, err := m.start(ctx, h, bin, platform, serverURL, origin, forward)
	if err != nil {
		return nil, err
	}
	conn.Email = email

	report(Stage{Name: "tunnel", Message: "Waiting for it to answer"})
	if err := m.waitReady(ctx, conn); err != nil {
		conn.close()
		return nil, err
	}
	m.mu.Lock()
	old := m.conns[id]
	m.conns[id] = conn
	m.mu.Unlock()
	if old != nil {
		old.close()
	}
	report(Stage{Name: "ready", Message: "Connected"})
	return conn, nil
}

// detect answers "linux/amd64" for the host.
func (m *Manager) detect(ctx context.Context, h Host) (string, error) {
	out, err := m.o.Client.Run(ctx, h, nil, "uname -sm")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return "", fmt.Errorf("could not tell what %s runs: %q", h.Destination, strings.TrimSpace(string(out)))
	}
	goos := map[string]string{"Linux": "linux", "Darwin": "darwin"}[fields[0]]
	goarch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[fields[1]]
	if goos == "" || goarch == "" {
		return "", fmt.Errorf("harness-client has no build for %s %s", fields[0], fields[1])
	}
	return goos + "/" + goarch, nil
}

// findOrDeploy returns the path of a client on the host that says what this client says: the one on the host's PATH or an
// uploaded one, else it uploads one. A host with a different client and nothing to upload keeps what it has.
func (m *Manager) findOrDeploy(ctx context.Context, h Host, platform string, report func(Stage)) (string, error) {
	c := m.o.Client
	// $HOME/.harness/bin first: it is what an earlier connection put there.
	probe := `for b in "` + RemoteBin + `" "$(command -v harness-client 2>/dev/null)"; do if [ -n "$b" ] && [ -x "$b" ]; then printf '%s\t' "$b"; "$b" version 2>/dev/null || echo; fi; done`
	out, err := c.Run(ctx, h, nil, probe)
	if err != nil {
		return "", err
	}
	var stale string
	for _, line := range strings.Split(string(out), "\n") {
		path, version, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || path == "" {
			continue
		}
		if strings.TrimSpace(version) == m.o.Version {
			report(Stage{Name: "deploy", Message: "Found harness-client on the machine"})
			return path, nil
		}
		if stale == "" {
			stale = path
		}
	}
	goos, goarch, _ := strings.Cut(platform, "/")
	binaries := m.o.Binaries
	if binaries == nil {
		binaries = OwnBinary
	}
	path, done, err := binaries(ctx, goos, goarch)
	if err != nil {
		if stale != "" {
			report(Stage{Name: "deploy", Message: "Using the version already on the machine: " + err.Error()})
			return stale, nil
		}
		return "", fmt.Errorf("%s has no harness-client and none could be found for %s: %w", h.Destination, platform, err)
	}
	if done != nil {
		defer done()
	}
	report(Stage{Name: "deploy", Message: "Uploading harness-client"})
	return m.upload(ctx, h, path)
}

// upload puts the file on the host in one move, after checking it arrived whole: nothing half-written ever runs.
func (m *Manager) upload(ctx context.Context, h Host, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	script := `set -e; d="$HOME/.harness/bin"; mkdir -p "$d"; chmod 700 "$HOME/.harness" "$d" 2>/dev/null || true; t="$d/.harness-client.$$"; trap 'rm -f "$t"' EXIT; cat > "$t"; ` +
		`s=$( (sha256sum "$t" 2>/dev/null || shasum -a 256 "$t") | cut -d' ' -f1); [ "$s" = ` + shellQuote(want) + ` ] || { echo "the upload arrived damaged" >&2; exit 1; }; chmod 755 "$t"; mv "$t" "$d/harness-client"; trap - EXIT; printf '%s' "$d/harness-client"`
	out, err := m.o.Client.Run(ctx, h, bytes.NewReader(data), script)
	if err != nil {
		return "", fmt.Errorf("uploading harness-client: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// whoami is the answer of `harness-client whoami --json`.
type whoami struct {
	SignedIn bool   `json:"signed_in"`
	Server   string `json:"server"`
	Email    string `json:"email"`
}

// signIn makes sure the host's client is signed in to this server, with the device code flow if not. The host gets a device of
// its own: nothing of this machine's credentials goes there.
func (m *Manager) signIn(ctx context.Context, h Host, bin, serverURL string, forward []string, report func(Stage)) (string, error) {
	c := m.o.Client
	out, err := c.Run(ctx, h, nil, shellQuote(bin)+" whoami --json", forward...)
	if err != nil {
		return "", fmt.Errorf("asking the client there who it is: %w", err)
	}
	var who whoami
	if json.Unmarshal(bytes.TrimSpace(out), &who) == nil && who.SignedIn && strings.TrimRight(who.Server, "/") == serverURL {
		return who.Email, nil
	}
	report(Stage{Name: "sign_in", Message: "Signing the client there in"})
	cmd := exec.CommandContext(ctx, c.sshPath(), append(c.args(h, forward...), shellQuote(bin)+" login --json "+shellQuote(serverURL))...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrNoSSH
		}
		return "", err
	}
	email, failed := "", ""
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		var ev struct {
			Event           string `json:"event"`
			VerificationURI string `json:"verification_uri"`
			UserCode        string `json:"user_code"`
			Email           string `json:"email"`
			Message         string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Event {
		case "code":
			report(Stage{Name: "sign_in", Message: "Confirm this code to sign the client there in", VerificationURI: ev.VerificationURI, UserCode: ev.UserCode})
		case "signed_in":
			email = ev.Email
		case "error":
			failed = ev.Message
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if failed != "" {
		return "", fmt.Errorf("signing in on %s: %s", h.Destination, failed)
	}
	if err != nil {
		return "", fmt.Errorf("signing in on %s: %s", h.Destination, tail(stderr.String()))
	}
	return email, nil
}

func randomPort() (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(20000))
	if err != nil {
		return 0, err
	}
	return 40000 + int(n.Int64()), nil
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// start runs the client on the host with a local port tunneled to it. The token travels over ssh's stdin, never on a command
// line another user of the host could read; stdin staying open is also what keeps the client there alive, so it goes away with
// the connection.
func (m *Manager) start(ctx context.Context, h Host, bin, platform, serverURL, origin string, forward []string) (*Connection, error) {
	c := m.o.Client
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(b)
	local, err := freeLocalPort()
	if err != nil {
		return nil, err
	}
	remote, err := randomPort()
	if err != nil {
		return nil, err
	}
	command := "IFS= read -r HARNESS_AGENT_TOKEN; export HARNESS_AGENT_TOKEN HARNESS_EXIT_WITH_STDIN=1; exec " + shellQuote(bin) + " serve --listen " + shellQuote("127.0.0.1:"+strconv.Itoa(remote))
	if origin != "" {
		command += " --cors-origin " + shellQuote(origin)
	}
	if serverURL != "" {
		command += " --server " + shellQuote(serverURL)
	}
	extra := append([]string{"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, remote), "-o", "ExitOnForwardFailure=yes"}, forward...)
	// Not tied to ctx: the connection outlives the call that made it.
	cmd := exec.Command(c.sshPath(), append(c.args(h, extra...), command)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = &syncWriter{w: stderr}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNoSSH
		}
		return nil, err
	}
	if _, err := io.WriteString(stdin, token+"\n"); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("handing the client there its token: %w", err)
	}
	conn := &Connection{HostID: h.ID, URL: "http://127.0.0.1:" + strconv.Itoa(local), Token: token, Platform: platform, cmd: cmd, stdin: stdin, done: make(chan struct{}), stderr: stderr}
	go func() {
		_ = cmd.Wait()
		close(conn.done)
	}()
	return conn, nil
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// waitReady polls the tunnel until the client there answers: a 401 without the token is an answer, and a tunnel with nothing behind
// it is a closed connection or a failure of another kind.
func (m *Manager) waitReady(ctx context.Context, conn *Connection) error {
	limit := m.o.Ready
	if limit <= 0 {
		limit = 30 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, conn.URL+"/harness.agent.v1.AgentService/Initialize", strings.NewReader("{}"))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+conn.Token)
		if res, err := client.Do(req); err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-conn.done:
			return fmt.Errorf("the client there stopped: %s", tail(conn.stderr.String()))
		case <-rctx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("the client there did not answer in time")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
