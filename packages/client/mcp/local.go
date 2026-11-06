package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
)

// LocalIDPrefix starts the id of a server from this device's own list.
const LocalIDPrefix = "mcl_"

// IsLocalID reports whether id names a server from this device's own list.
func IsLocalID(id string) bool { return strings.HasPrefix(id, LocalIDPrefix) }

// LocalStore is this device's own list of MCP servers: a file in HARNESS_HOME, mode 0600, never sent to the harness
// server. Secret values sit in the same file, referenced from the config as `${secret:NAME}` like in the registry,
// and are never returned by a read: only List's names and launchData (for spawning) use them. Permission rules for
// these servers stay here too, because the synced settings would carry their names to other devices.
type LocalStore struct {
	Path string

	mu sync.Mutex
}

type localFile struct {
	Servers []localEntry `json:"servers"`
	Allow   []string     `json:"allow,omitempty"`
	Ask     []string     `json:"ask,omitempty"`
	Deny    []string     `json:"deny,omitempty"`
}

type localEntry struct {
	Config  json.RawMessage   `json:"config"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

func (l *LocalStore) load() (localFile, error) {
	var f localFile
	if l == nil || l.Path == "" {
		return f, nil
	}
	data, err := os.ReadFile(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("%s: %w", l.Path, err)
	}
	return f, nil
}

func (l *LocalStore) save(f localFile) error {
	if l == nil || l.Path == "" {
		return errors.New("this client has no directory for its own MCP servers")
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.Path), ".mcp-local-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.Path)
}

func (e localEntry) server() (*mcpv1.Server, error) {
	s := &mcpv1.Server{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(e.Config, s); err != nil {
		return nil, err
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range e.Secrets {
			if !yield(k) {
				return
			}
		}
	})
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
		s.Secrets = append(s.Secrets, &mcpv1.SecretInfo{Name: n, HasValue: true})
	}
	for _, n := range secretNames(s) {
		if !seen[n] {
			seen[n] = true
			s.Secrets = append(s.Secrets, &mcpv1.SecretInfo{Name: n})
		}
	}
	s.ConfigHash = localConfigHash(s)
	s.CanEdit = true
	return s, nil
}

// List returns the servers, enabled or not, with secret names but no values. A file that cannot be read yields none.
func (l *LocalStore) List() []*mcpv1.Server {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return nil
	}
	out := make([]*mcpv1.Server, 0, len(f.Servers))
	for _, e := range f.Servers {
		if s, err := e.server(); err == nil {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// Get returns one server without secret values.
func (l *LocalStore) Get(id string) *mcpv1.Server {
	for _, s := range l.List() {
		if s.GetId() == id {
			return s
		}
	}
	return nil
}

// Create adds a server and returns it. Scope and read-only fields of s are ignored.
func (l *LocalStore) Create(s *mcpv1.Server, ch *mcpv1.SecretChanges) (*mcpv1.Server, error) {
	if l == nil {
		return nil, errors.New("this client has no directory for its own MCP servers")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return nil, err
	}
	c := proto.Clone(s).(*mcpv1.Server)
	id, err := newLocalID()
	if err != nil {
		return nil, err
	}
	c.Id = id
	e, err := buildEntry(c, nil, ch)
	if err != nil {
		return nil, err
	}
	if err := nameFree(f, c.GetName(), c.GetId()); err != nil {
		return nil, err
	}
	f.Servers = append(f.Servers, e)
	if err := l.save(f); err != nil {
		return nil, err
	}
	return e.server()
}

// Update replaces a server's config. Unnamed secrets stay.
func (l *LocalStore) Update(s *mcpv1.Server, ch *mcpv1.SecretChanges) (*mcpv1.Server, error) {
	if l == nil {
		return nil, errors.New("this client has no directory for its own MCP servers")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return nil, err
	}
	for i, e := range f.Servers {
		cur, err := e.server()
		if err != nil || cur.GetId() != s.GetId() {
			continue
		}
		next, err := buildEntry(s, e.Secrets, ch)
		if err != nil {
			return nil, err
		}
		if err := nameFree(f, s.GetName(), s.GetId()); err != nil {
			return nil, err
		}
		f.Servers[i] = next
		if err := l.save(f); err != nil {
			return nil, err
		}
		return next.server()
	}
	return nil, fmt.Errorf("no MCP server %s on this device", s.GetId())
}

// Delete removes a server and its secrets.
func (l *LocalStore) Delete(id string) error {
	if l == nil {
		return errors.New("this client has no directory for its own MCP servers")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return err
	}
	for i, e := range f.Servers {
		if cur, err := e.server(); err == nil && cur.GetId() == id {
			f.Servers = slices.Delete(f.Servers, i, i+1)
			return l.save(f)
		}
	}
	return fmt.Errorf("no MCP server %s on this device", id)
}

// Rules returns the permission rules kept for this device's servers.
func (l *LocalStore) Rules() (allow, ask, deny []string) {
	if l == nil {
		return nil, nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return nil, nil, nil
	}
	return f.Allow, f.Ask, f.Deny
}

// SetRule makes rule ("allow", "ask", "deny", "" for none) the decision for the rule string.
func (l *LocalStore) SetRule(ruleString, decision string) error {
	if l == nil {
		return errors.New("this client has no directory for its own MCP servers")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return err
	}
	drop := func(list []string) []string {
		return slices.DeleteFunc(list, func(s string) bool { return s == ruleString })
	}
	f.Allow, f.Ask, f.Deny = drop(f.Allow), drop(f.Ask), drop(f.Deny)
	switch decision {
	case "allow":
		f.Allow = append(f.Allow, ruleString)
	case "ask":
		f.Ask = append(f.Ask, ruleString)
	case "deny":
		f.Deny = append(f.Deny, ruleString)
	}
	return l.save(f)
}

// launchData resolves a stdio server for spawning (secrets substituted). Keep the result in memory only.
func (l *LocalStore) launchData(id string) (*mcpv1.IssueStdioLaunchResponse, error) {
	cfg, secrets, err := l.resolved(id)
	if err != nil {
		return nil, err
	}
	sub := func(v string) (string, error) { return substituteSecrets(v, secrets) }
	st := cfg.GetStdio()
	out := &mcpv1.IssueStdioLaunchResponse{Env: map[string]string{}}
	if out.Command, err = sub(st.GetCommand()); err != nil {
		return nil, err
	}
	for _, a := range st.GetArgs() {
		v, err := sub(a)
		if err != nil {
			return nil, err
		}
		out.Args = append(out.Args, v)
	}
	for k, v := range st.GetEnv() {
		if out.Env[k], err = sub(v); err != nil {
			return nil, err
		}
	}
	if out.Cwd, err = sub(st.GetCwd()); err != nil {
		return nil, err
	}
	return out, nil
}

// remoteData resolves a device-local HTTP or SSE server: its URL and headers with secrets substituted.
func (l *LocalStore) remoteData(id string) (string, map[string]string, error) {
	cfg, secrets, err := l.resolved(id)
	if err != nil {
		return "", nil, err
	}
	u, err := substituteSecrets(cfg.GetRemote().GetUrl(), secrets)
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{}
	for k, v := range cfg.GetRemote().GetHeaders() {
		if headers[k], err = substituteSecrets(v, secrets); err != nil {
			return "", nil, err
		}
	}
	return u, headers, nil
}

func (l *LocalStore) resolved(id string) (*mcpv1.Server, map[string]string, error) {
	if l == nil {
		return nil, nil, errors.New("this client has no directory for its own MCP servers")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.load()
	if err != nil {
		return nil, nil, err
	}
	for _, e := range f.Servers {
		if s, err := e.server(); err == nil && s.GetId() == id {
			return s, e.Secrets, nil
		}
	}
	return nil, nil, fmt.Errorf("no MCP server %s on this device", id)
}

func nameFree(f localFile, name, id string) error {
	for _, e := range f.Servers {
		if s, err := e.server(); err == nil && s.GetName() == name && s.GetId() != id {
			return fmt.Errorf("an MCP server named %s already exists on this device", name)
		}
	}
	return nil
}

var (
	localNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	secretNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretRefRe   = regexp.MustCompile(`\$\{secret:([^}]*)\}`)
	envNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNameRe  = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	errNoWSDirect = errors.New("WebSocket servers go through the xlsft`s harness server: add it to your account instead")
)

// buildEntry validates a config and applies secret changes over `old`.
func buildEntry(s *mcpv1.Server, old map[string]string, ch *mcpv1.SecretChanges) (localEntry, error) {
	if !localNameRe.MatchString(s.GetName()) {
		return localEntry{}, errors.New("name must be 1-64 characters of [A-Za-z0-9_.-], starting with a letter or digit")
	}
	stored := &mcpv1.Server{Id: s.GetId(), Name: s.GetName(), Transport: s.GetTransport(), Enabled: s.GetEnabled(), AlwaysLoad: s.GetAlwaysLoad(),
		ToolTimeoutMs: s.GetToolTimeoutMs(), AllowedTools: s.GetAllowedTools(), DeniedTools: s.GetDeniedTools(), MaxOutputChars: s.GetMaxOutputChars()}
	switch s.GetTransport() {
	case mcpv1.Transport_TRANSPORT_STDIO:
		if strings.TrimSpace(s.GetStdio().GetCommand()) == "" {
			return localEntry{}, errors.New("a command is required")
		}
		for k := range s.GetStdio().GetEnv() {
			if !envNameRe.MatchString(k) {
				return localEntry{}, fmt.Errorf("invalid environment variable name %q", k)
			}
		}
		stored.Stdio = s.GetStdio()
	case mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP, mcpv1.Transport_TRANSPORT_SSE:
		raw := secretRefRe.ReplaceAllString(s.GetRemote().GetUrl(), "x")
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return localEntry{}, errors.New("the URL must be an absolute http(s) URL")
		}
		if u.User != nil {
			return localEntry{}, errors.New("credentials in the URL are not allowed; use a header with ${secret:NAME}")
		}
		for k := range s.GetRemote().GetHeaders() {
			if !headerNameRe.MatchString(k) {
				return localEntry{}, fmt.Errorf("invalid header name %q", k)
			}
		}
		stored.Remote = s.GetRemote()
	case mcpv1.Transport_TRANSPORT_WS:
		return localEntry{}, errNoWSDirect
	default:
		return localEntry{}, errors.New("a transport is required")
	}
	for _, n := range secretNames(stored) {
		if !secretNameRe.MatchString(n) {
			return localEntry{}, fmt.Errorf("invalid secret reference ${secret:%s}", n)
		}
	}
	secrets := map[string]string{}
	for k, v := range old {
		secrets[k] = v
	}
	for k, v := range ch.GetSet() {
		if !secretNameRe.MatchString(k) {
			return localEntry{}, fmt.Errorf("invalid secret name %q", k)
		}
		secrets[k] = v
	}
	for _, k := range ch.GetRemove() {
		delete(secrets, k)
	}
	raw, err := protojson.Marshal(stored)
	if err != nil {
		return localEntry{}, err
	}
	return localEntry{Config: raw, Secrets: secrets}, nil
}

// secretNames lists the `${secret:NAME}` references of a config, sorted.
func secretNames(s *mcpv1.Server) []string {
	var parts []string
	st := s.GetStdio()
	parts = append(parts, st.GetCommand(), st.GetCwd())
	parts = append(parts, st.GetArgs()...)
	for _, v := range st.GetEnv() {
		parts = append(parts, v)
	}
	r := s.GetRemote()
	parts = append(parts, r.GetUrl())
	for _, v := range r.GetHeaders() {
		parts = append(parts, v)
	}
	var out []string
	for _, p := range parts {
		for _, m := range secretRefRe.FindAllStringSubmatch(p, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	sort.Strings(out)
	return out
}

func substituteSecrets(v string, secrets map[string]string) (string, error) {
	var missing string
	out := secretRefRe.ReplaceAllStringFunc(v, func(m string) string {
		name := secretRefRe.FindStringSubmatch(m)[1]
		val, ok := secrets[name]
		if !ok {
			missing = name
		}
		return val
	})
	if missing != "" {
		return "", fmt.Errorf("secret %s is not set", missing)
	}
	return out, nil
}

// localConfigHash binds this device's approval to what would run or be contacted (never to secret values).
func localConfigHash(s *mcpv1.Server) string {
	names := make([]string, 0, len(s.GetSecrets()))
	for _, si := range s.GetSecrets() {
		names = append(names, si.GetName())
	}
	sort.Strings(names)
	body, _ := json.Marshal(map[string]any{
		"transport": s.GetTransport().String(),
		"command":   s.GetStdio().GetCommand(),
		"args":      s.GetStdio().GetArgs(),
		"env":       s.GetStdio().GetEnv(),
		"cwd":       s.GetStdio().GetCwd(),
		"url":       s.GetRemote().GetUrl(),
		"headers":   s.GetRemote().GetHeaders(),
		"helper":    s.GetRemote().GetHeadersHelper(),
		"secrets":   names,
	})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func newLocalID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return LocalIDPrefix + hex.EncodeToString(b[:]), nil
}
