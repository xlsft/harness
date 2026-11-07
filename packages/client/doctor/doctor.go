// Package doctor checks what harness-client needs to work on this machine: the sign-in and the server, version skew and the
// organization's version policy, the keychain, the command sandbox, MCP servers, disk space for transcripts, git and the
// proxy settings. `harness-client doctor` prints the report and Settings > About shows it (AgentService.RunDoctor).
//
// Every check only reads, except the MCP one, which starts the servers that would start in a session anyway (approved,
// enabled, allowed), to see that they do. Each dependency is a field of Options, so tests run on fakes.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/sandbox"
	"github.com/xlsft/harness/packages/client/selfupdate"
)

// Status of a check.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

func (s Status) rank() int {
	switch s {
	case Fail:
		return 3
	case Warn:
		return 2
	case Pass:
		return 1
	}
	return 0
}

// Check is one line of the report.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Report is every check, in a fixed order.
type Report struct {
	ClientVersion string  `json:"client_version"`
	Status        Status  `json:"status"`
	Checks        []Check `json:"checks"`
}

// MCPResult is what starting one MCP server gave.
type MCPResult struct {
	Name string
	// OK: it started and answered. Skipped: it isn't started in a session either (disabled, needs approval or sign-in);
	// Detail says why. Otherwise it failed, and Detail is the error.
	OK, Skipped bool
	Detail      string
}

// Options are the checks' dependencies. A nil function skips what needs it.
type Options struct {
	ClientVersion string
	// Credentials says where the sign-in is kept (auth.CredentialsWhere) and the server it is for; "" when not signed in.
	Server      string
	Credentials func() (where string, keychainErr error)
	// Me asks the server who is signed in.
	Me func(ctx context.Context) (email string, err error)
	// HTTP reaches the server.
	HTTP *http.Client
	// Policy is the organization's managed policy as this client knows it; fresh says whether the server was asked.
	Policy func(ctx context.Context) (m permissions.ManagedPolicy, fresh bool)
	// Sandbox is the command sandbox of this platform.
	Sandbox func() sandbox.Info
	// MCP starts the MCP servers a session would and says how each went. Nil: the check is skipped.
	MCP func(ctx context.Context) ([]MCPResult, error)
	// DataDir holds transcripts (config.Home()); DiskFree reports the bytes free on its file system.
	DataDir  string
	DiskFree func(path string) (uint64, error)
	// LookPath and Run find and run git (exec.LookPath, a command's output).
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, name string, args ...string) (string, error)
	// Getenv reads the proxy variables.
	Getenv func(string) string
	GOOS   string
}

// Defaults fills what a real machine has: the sandbox driver, disk space, git, the environment.
func Defaults(o Options) Options {
	if o.Sandbox == nil {
		o.Sandbox = func() sandbox.Info { return sandbox.NewDriver().Info() }
	}
	if o.DiskFree == nil {
		o.DiskFree = diskFree
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).Output()
			return strings.TrimSpace(string(out)), err
		}
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return o
}

// Run runs every check. The slow ones (server, MCP) run side by side.
func Run(ctx context.Context, o Options) Report {
	o = Defaults(o)
	r := Report{ClientVersion: o.ClientVersion}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = map[string]Check{}
	)
	add := func(c Check) {
		mu.Lock()
		results[c.ID] = c
		mu.Unlock()
	}
	var serverVersion string
	wg.Add(3)
	go func() {
		defer wg.Done()
		add(checkSignIn(ctx, o))
	}()
	go func() {
		defer wg.Done()
		c, v := checkServer(ctx, o)
		add(c)
		mu.Lock()
		serverVersion = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		add(checkMCP(ctx, o))
	}()
	var policy permissions.ManagedPolicy
	var fresh bool
	if o.Policy != nil {
		policy, fresh = o.Policy(ctx)
	}
	add(checkKeychain(o))
	add(checkSandbox(o, policy))
	add(checkDisk(o))
	add(checkGit(ctx, o))
	add(checkProxy(o))
	wg.Wait()
	add(checkVersion(o, serverVersion))
	add(checkVersionPolicy(o, policy, fresh))
	for _, id := range Order {
		if c, ok := results[id]; ok {
			r.Checks = append(r.Checks, c)
		}
	}
	r.Status = Pass
	for _, c := range r.Checks {
		if c.Status.rank() > r.Status.rank() {
			r.Status = c.Status
		}
	}
	return r
}

// Order is the order of the report, and the ids of AgentService's DoctorCheck.
var Order = []string{"signin", "server", "version", "version_policy", "keychain", "sandbox", "mcp", "disk", "git", "proxy"}

func host(server string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return server
}

func checkSignIn(ctx context.Context, o Options) Check {
	c := Check{ID: "signin", Title: "Sign-in"}
	if o.Server == "" {
		c.Status, c.Detail, c.Hint = Fail, "Not signed in to an xlsft`s harness server", "harness-client login https://your-harness-server"
		return c
	}
	if o.Me == nil {
		c.Status, c.Detail = Skip, "Signed in to "+host(o.Server)+"; not confirmed"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	email, err := o.Me(ctx)
	switch {
	case err == nil:
		c.Status, c.Detail = Pass, fmt.Sprintf("Signed in to %s as %s", host(o.Server), email)
	case errors.Is(err, auth.ErrNotLoggedIn) || errors.Is(err, auth.ErrDeviceRevoked):
		c.Status, c.Detail, c.Hint = Fail, fmt.Sprintf("The sign-in to %s is no longer valid: %v", host(o.Server), err), "harness-client login "+o.Server
	default:
		c.Status, c.Detail, c.Hint = Warn, fmt.Sprintf("Signed in to %s, but the server didn't confirm it: %v", host(o.Server), err), "See the server check below"
	}
	return c
}

// checkServer reads /api/health and returns the server's version.
func checkServer(ctx context.Context, o Options) (Check, string) {
	c := Check{ID: "server", Title: "Server"}
	if o.Server == "" {
		c.Status, c.Detail = Skip, "No server to reach: not signed in"
		return c, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.Server, "/")+"/api/health", nil)
	if err != nil {
		c.Status, c.Detail = Fail, err.Error()
		return c, ""
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		c.Status, c.Detail = Fail, fmt.Sprintf("%s can't be reached: %v", host(o.Server), err)
		c.Hint = "Check the address, the network and the proxy settings (below)"
		return c, ""
	}
	defer resp.Body.Close()
	version := ""
	if resp.StatusCode == http.StatusOK {
		version = readHealthVersion(resp)
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	if resp.StatusCode != http.StatusOK {
		c.Status, c.Detail = Fail, fmt.Sprintf("%s answered %s to /api/health", host(o.Server), resp.Status)
		c.Hint = "Is this an xlsft`s harness server? Its admin can check `docker logs harness`"
		return c, ""
	}
	c.Status = Pass
	c.Detail = fmt.Sprintf("%s answers in %s", host(o.Server), elapsed)
	if version != "" {
		c.Detail += ", version " + version
	}
	if elapsed > 3*time.Second {
		c.Status, c.Hint = Warn, "Slow: every model request goes through the server"
	}
	return c, version
}

// checkVersion compares the client with the server: within one minor version is what `buf breaking` keeps working.
func checkVersion(o Options, server string) Check {
	c := Check{ID: "version", Title: "Version skew"}
	client := o.ClientVersion
	switch {
	case server == "":
		c.Status, c.Detail = Skip, "harness-client "+client+"; the server's version is unknown"
		return c
	case selfupdate.IsDevBuild(client) || selfupdate.IsDevBuild(server):
		c.Status, c.Detail = Pass, fmt.Sprintf("harness-client %s, server %s: a development build, not compared", client, server)
		return c
	}
	cx, cy, ok1 := minor(client)
	sx, sy, ok2 := minor(server)
	if !ok1 || !ok2 {
		c.Status, c.Detail = Warn, fmt.Sprintf("harness-client %s, server %s: can't compare", client, server)
		return c
	}
	c.Detail = fmt.Sprintf("harness-client %s, server %s", client, server)
	switch {
	case cx == sx && cy == sy:
		c.Status = Pass
	case cx == sx && sy-cy == 1:
		// buf breaking keeps the contracts backward compatible within v1: one minor version behind keeps working.
		c.Status = Pass
		c.Detail += ": one minor version behind, which keeps working"
	case cx < sx || (cx == sx && cy < sy):
		c.Status, c.Hint = Warn, "Update the client: harness-client update (the app: Settings > About)"
		c.Detail += ": the client is more than a minor version behind"
	default:
		c.Status, c.Hint = Warn, "Its admin updates the server (deploy/README.md, Upgrading)"
		c.Detail += ": the client is newer than the server"
	}
	return c
}

// minor reads the release line of a version: X and Y of X.Y.Z.
func minor(v string) (x, y int, ok bool) {
	var z int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	if n, err := fmt.Sscanf(core, "%d.%d.%d", &x, &y, &z); err != nil || n != 3 {
		return 0, 0, false
	}
	return x, y, true
}

// readHealthVersion reads {"version": ...} of /api/health.
func readHealthVersion(resp *http.Response) string {
	var health struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&health) != nil {
		return ""
	}
	return health.Version
}

func checkVersionPolicy(o Options, m permissions.ManagedPolicy, fresh bool) Check {
	c := Check{ID: "version_policy", Title: "Version policy"}
	b := selfupdate.Bounds{Min: m.MinClientVersion, Max: m.MaxClientVersion}
	if !b.Set() {
		c.Status, c.Detail = Pass, "Your organization sets no client versions"
		if o.Policy == nil {
			c.Status, c.Detail = Skip, "The managed policy wasn't read"
		}
		return c
	}
	var bounds []string
	if b.Min != "" {
		bounds = append(bounds, "at least "+b.Min)
	}
	if b.Max != "" {
		bounds = append(bounds, "at most "+b.Max)
	}
	c.Detail = "Your administrator allows " + strings.Join(bounds, " and ")
	if !fresh {
		c.Detail += " (as last known: the server wasn't asked)"
	}
	switch b.Check(o.ClientVersion) {
	case selfupdate.Below:
		c.Status, c.Hint = Fail, "harness-client update (the app: Settings > About)"
		c.Detail += "; this is " + o.ClientVersion
	case selfupdate.Above:
		c.Status, c.Hint = Warn, "It keeps working; updates stop at "+b.Max+". Ask your administrator if this version should be allowed"
		c.Detail += "; this is " + o.ClientVersion
	default:
		c.Status = Pass
		c.Detail += "; this is " + o.ClientVersion
	}
	return c
}

func checkKeychain(o Options) Check {
	c := Check{ID: "keychain", Title: "Keychain"}
	if o.Credentials == nil {
		c.Status, c.Detail = Skip, "Not checked"
		return c
	}
	where, kerr := o.Credentials()
	name := "the keychain"
	if o.GOOS == "darwin" {
		name = "the login keychain"
	}
	switch {
	case where == auth.InKeychain:
		c.Status, c.Detail = Pass, "The sign-in is kept in "+name
	case where == auth.InFile && kerr != nil:
		c.Status, c.Detail = Warn, fmt.Sprintf("The keychain can't be used (%v), so the sign-in is in a file only you can read", kerr)
		c.Hint = "On a desktop, unlock the keychain and sign in again; on a server that's expected"
	case where == auth.InFile:
		c.Status, c.Detail = Pass, "The sign-in is in a file only you can read (HARNESS_KEYRING=0, or no keychain here)"
	case kerr != nil:
		c.Status, c.Detail = Warn, fmt.Sprintf("The keychain can't be used: %v; a sign-in would go to a file", kerr)
	default:
		c.Status, c.Detail = Skip, "Nothing stored: not signed in"
	}
	return c
}

func checkSandbox(o Options, m permissions.ManagedPolicy) Check {
	c := Check{ID: "sandbox", Title: "Sandbox"}
	info := o.Sandbox()
	if info.Supported {
		c.Status, c.Detail = Pass, "Commands can run in the "+sandboxName(info.Platform)+" sandbox"
		return c
	}
	c.Detail = "No command sandbox here"
	if info.Reason != "" {
		c.Detail += ": " + info.Reason
	}
	if m.SandboxRequired {
		c.Status, c.Hint = Fail, "Your administrator requires the sandbox, so commands can't run on this machine"
	} else {
		c.Status, c.Hint = Warn, "Commands run with your rights; permission rules still apply"
	}
	return c
}

func sandboxName(platform string) string {
	switch platform {
	case "macos":
		return "Seatbelt"
	case "linux":
		return "Linux"
	}
	return platform
}

func checkMCP(ctx context.Context, o Options) Check {
	c := Check{ID: "mcp", Title: "MCP servers"}
	if o.MCP == nil {
		c.Status, c.Detail = Skip, "Not checked"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	results, err := o.MCP(ctx)
	if err != nil && o.Server == "" {
		c.Status, c.Detail = Skip, "Not signed in, and this device has no servers of its own"
		return c
	}
	if err != nil {
		c.Status, c.Detail = Warn, "The list of servers couldn't be read: "+err.Error()
		return c
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	var ok, skipped int
	var failed []string
	for _, r := range results {
		switch {
		case r.OK:
			ok++
		case r.Skipped:
			skipped++
		default:
			failed = append(failed, r.Name+": "+r.Detail)
		}
	}
	if len(results) == 0 {
		c.Status, c.Detail = Pass, "No MCP servers configured"
		return c
	}
	parts := []string{fmt.Sprintf("%d started", ok)}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d not started (disabled, awaiting approval or sign-in)", skipped))
	}
	if len(failed) > 0 {
		c.Status = Warn
		c.Detail = fmt.Sprintf("%d of %d failed to start: %s", len(failed), len(results), strings.Join(failed, "; "))
		c.Hint = "Extensions > Connectors shows each server's error"
		return c
	}
	c.Status, c.Detail = Pass, strings.Join(parts, ", ")
	return c
}

const (
	diskFail = 500 << 20
	diskWarn = 2 << 30
)

func checkDisk(o Options) Check {
	c := Check{ID: "disk", Title: "Disk space"}
	if o.DataDir == "" {
		c.Status, c.Detail = Skip, "Not checked"
		return c
	}
	dir := o.DataDir
	// Before the first session the folder may not exist: its nearest existing parent is on the same disk.
	for {
		if _, err := os.Stat(dir); err == nil || dir == "/" || dir == "." {
			break
		}
		dir = parent(dir)
	}
	free, err := o.DiskFree(dir)
	if err != nil {
		c.Status, c.Detail = Skip, "Free space unknown: "+err.Error()
		return c
	}
	c.Detail = fmt.Sprintf("%s free for transcripts in %s", humanBytes(free), o.DataDir)
	switch {
	case free < diskFail:
		c.Status, c.Hint = Fail, "Sessions can't be saved reliably; free some space"
	case free < diskWarn:
		c.Status, c.Hint = Warn, "Long sessions and their attachments may not fit; free some space"
	default:
		c.Status = Pass
	}
	return c
}

func parent(p string) string {
	i := strings.LastIndexAny(strings.TrimRight(p, `/\`), `/\`)
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", n>>10)
}

func checkGit(ctx context.Context, o Options) Check {
	c := Check{ID: "git", Title: "git"}
	path, err := o.LookPath("git")
	if err != nil {
		c.Status, c.Detail = Warn, "git isn't installed: worktrees, the changes pane and pull requests need it"
		c.Hint = "Install git"
		if o.GOOS == "darwin" {
			c.Hint = "xcode-select --install"
		}
		return c
	}
	// /usr/bin/git on a Mac without the Command Line Tools is a stub that opens an installer: ask xcode-select first.
	if o.GOOS == "darwin" && path == "/usr/bin/git" {
		if _, err := o.Run(ctx, "/usr/bin/xcode-select", "-p"); err != nil {
			c.Status, c.Detail, c.Hint = Warn, "git needs the Command Line Tools, which aren't installed", "xcode-select --install"
			return c
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := o.Run(ctx, path, "--version")
	if err != nil {
		c.Status, c.Detail = Warn, fmt.Sprintf("%s doesn't run: %v", path, err)
		return c
	}
	c.Status, c.Detail = Pass, strings.TrimPrefix(out, "git version ")+" at "+path
	return c
}

func checkProxy(o Options) Check {
	c := Check{ID: "proxy", Title: "Proxy"}
	get := func(name string) string {
		if v := o.Getenv(name); v != "" {
			return v
		}
		return o.Getenv(strings.ToLower(name))
	}
	cfg := &httpproxy.Config{HTTPProxy: get("HTTP_PROXY"), HTTPSProxy: get("HTTPS_PROXY"), NoProxy: get("NO_PROXY")}
	var set []string
	for _, kv := range [][2]string{{"HTTPS_PROXY", cfg.HTTPSProxy}, {"HTTP_PROXY", cfg.HTTPProxy}} {
		if kv[1] != "" {
			set = append(set, kv[0]+"="+redact(kv[1]))
		}
	}
	if cfg.NoProxy != "" {
		set = append(set, "NO_PROXY="+cfg.NoProxy)
	}
	all := get("ALL_PROXY")
	if len(set) == 0 && all == "" {
		c.Status, c.Detail = Pass, "No proxy configured: connections go direct"
		return c
	}
	c.Status = Pass
	c.Detail = strings.Join(set, ", ")
	if o.Server != "" {
		if u, err := url.Parse(o.Server); err == nil {
			if p, err := cfg.ProxyFunc()(u); err != nil {
				c.Status, c.Hint = Warn, "A proxy variable doesn't parse: "+err.Error()
			} else if p != nil {
				c.Detail += "; the server is reached through " + redact(p.String())
			} else {
				c.Detail += "; the server is reached directly"
			}
		}
	}
	if all != "" && cfg.HTTPSProxy == "" && cfg.HTTPProxy == "" {
		c.Status = Warn
		c.Detail = "ALL_PROXY=" + redact(all) + " is set, but harness-client follows HTTPS_PROXY and HTTP_PROXY only"
		c.Hint = "Set HTTPS_PROXY to the same proxy"
	}
	return c
}

// redact hides a password in a proxy URL.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}
