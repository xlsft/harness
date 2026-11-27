package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
)

// MCP servers found in other tools' configs. At user level they become servers of this device (Settings > MCP, through
// the same path as adding one by hand: mcp.Inventory.Create), never sent to the xlsft`s harness server: they come from
// this machine's tools, may point at localhost, and their secrets stay here. In a folder they are merged into the
// folder's .mcp.json, where they need the folder trusted and this device's approval like any repository server.
//
// Secrets: an env, header, argument or URL query value that looks like a secret is stored as a secret of the server
// (`${secret:NAME}`) only with IncludeSecrets; without it the reference is created with no value, and the note names
// it. A `${VAR}` / `${env:VAR}` / `${input:ID}` the tool would have filled in becomes a secret reference with no value.

// MCPTarget is where user-level MCP servers go (implemented over mcp.Inventory by the server package).
type MCPTarget interface {
	// Servers lists every server this client knows, whoever manages it.
	Servers(ctx context.Context) ([]ExistingMCP, error)
	// Create adds a server to this device.
	Create(ctx context.Context, s *mcpv1.Server, secrets *mcpv1.SecretChanges) error
	// Replace replaces the config of an existing server (an import with force).
	Replace(ctx context.Context, id string, s *mcpv1.Server, secrets *mcpv1.SecretChanges) error
}

// ExistingMCP is a server that is already there.
type ExistingMCP struct {
	ID, Name, ManagedBy string
	// CanEdit: Replace may change it.
	CanEdit bool
}

var (
	mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	// ${NAME}, ${env:NAME}, ${NAME:-default}, ${input:id}.
	varRefRe   = regexp.MustCompile(`\$\{(env:|input:)?([A-Za-z_][A-Za-z0-9_.-]*)(:-[^}]*)?\}`)
	bareVarRe  = regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)$`)
	secretFlag = regexp.MustCompile(`(?i)(key|token|secret|passw)`)
)

// MCPName makes a server name harness accepts (1-64 of [A-Za-z0-9_.-], starting with a letter or digit).
func MCPName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), "_.-")
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "server"
	}
	return out
}

// secretName turns a key into a secret name: [A-Za-z_][A-Za-z0-9_]*, upper case.
func secretName(key string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(key) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "_" + s
	}
	return s
}

// mcpPlan is a user-level server as it would be created.
type mcpPlan struct {
	server  *mcpv1.Server
	secrets *mcpv1.SecretChanges
	notes   []string
	// hasSecrets: values that look like secrets were found (Include secrets matters for this item).
	hasSecrets bool
	// manual: harness cannot create it; the note says why.
	manual string
}

type secretBook struct {
	include bool
	home    string
	set     map[string]string
	empty   []string // secret values left out
	unset   []string // references the tool filled in itself
	names   map[string]bool
}

func (b *secretBook) name(key string) string {
	n := secretName(key)
	base, i := n, 2
	for b.names[n] {
		n = base + "_" + strconv.Itoa(i)
		i++
	}
	b.names[n] = true
	return n
}

// secret stores v as a secret named after key and returns the reference.
func (b *secretBook) secret(key, v string) string {
	n := b.name(key)
	if b.include {
		b.set[n] = v
	} else {
		b.empty = append(b.empty, n)
	}
	return "${secret:" + n + "}"
}

// refs rewrites the variables a tool expands into secret references with no value.
func (b *secretBook) refs(v string) string {
	if m := bareVarRe.FindStringSubmatch(v); m != nil {
		return b.ref(m[1])
	}
	return varRefRe.ReplaceAllStringFunc(v, func(m string) string {
		g := varRefRe.FindStringSubmatch(m)
		switch {
		case g[1] == "" && g[2] == "userHome" && b.home != "":
			return b.home
		case g[1] == "" && (g[2] == "pathSeparator" || g[2] == "/"):
			return "/"
		case strings.HasPrefix(g[2], "secret"):
			return m
		}
		return b.ref(g[2])
	})
}

func (b *secretBook) ref(name string) string {
	n := secretName(name)
	if !b.names[n] {
		b.names[n] = true
		b.unset = append(b.unset, n)
	}
	return "${secret:" + n + "}"
}

// value handles one env or header value.
func (b *secretBook) value(key, v string) string {
	if strings.Contains(v, "$") {
		if r := b.refs(v); r != v {
			return r
		}
	}
	if v != "" && isSecret(key, v) {
		return b.secret(key, v)
	}
	return v
}

// planMCP turns another tool's server into one of this device.
func planMCP(name string, m *MCPServer, includeSecrets bool, home, secretsHint string) mcpPlan {
	p := mcpPlan{}
	harnessName := MCPName(name)
	if harnessName != name {
		p.notes = append(p.notes, "named "+harnessName+" here")
	}
	b := &secretBook{include: includeSecrets, home: home, set: map[string]string{}, names: map[string]bool{}}
	s := &mcpv1.Server{Name: harnessName, Enabled: !m.Disabled}
	if m.Disabled {
		p.notes = append(p.notes, "switched off, as it was")
	}
	switch t := m.transport(); t {
	case "stdio":
		if strings.TrimSpace(m.Command) == "" {
			p.manual = "no command to run"
			return p
		}
		st := &mcpv1.StdioConfig{Command: b.refs(m.Command), Cwd: b.refs(m.Cwd), Env: map[string]string{}}
		flagged := false
		for i, a := range m.Args {
			switch {
			case strings.Contains(a, "${") || bareVarRe.MatchString(a):
				a = b.refs(a)
			case flagged && !strings.HasPrefix(a, "-"):
				a = b.secret("ARG_"+strconv.Itoa(i+1), a)
			case secretValue.MatchString(strings.TrimSpace(a)):
				a = b.secret("ARG_"+strconv.Itoa(i+1), a)
			default:
				if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "-") && v != "" && secretFlag.MatchString(k) {
					a = k + "=" + b.secret(strings.TrimLeft(k, "-"), v)
				}
			}
			flagged = strings.HasPrefix(a, "-") && !strings.Contains(a, "=") && secretFlag.MatchString(a)
			st.Args = append(st.Args, a)
		}
		for _, k := range sortedKeys(m.Env) {
			st.Env[k] = b.value(k, m.Env[k])
		}
		s.Transport, s.Stdio = mcpv1.Transport_TRANSPORT_STDIO, st
	case "http", "sse":
		if strings.TrimSpace(m.URL) == "" {
			p.manual = "no URL"
			return p
		}
		r := &mcpv1.RemoteConfig{Url: b.queryURL(b.refs(m.URL)), Headers: map[string]string{}}
		for _, k := range sortedKeys(m.Headers) {
			r.Headers[k] = b.value(k, m.Headers[k])
		}
		s.Transport, s.Remote = mcpv1.Transport_TRANSPORT_STREAMABLE_HTTP, r
		if t == "sse" {
			s.Transport = mcpv1.Transport_TRANSPORT_SSE
		}
	case "ws":
		p.manual = "a WebSocket server: add it to your account in Settings > MCP"
		return p
	default:
		p.manual = "transport " + t + " is not one harness speaks"
		return p
	}
	p.server = s
	if len(b.set) > 0 {
		p.secrets = &mcpv1.SecretChanges{Set: b.set}
	}
	p.hasSecrets = len(b.set)+len(b.empty) > 0
	if len(b.set) > 0 {
		p.notes = append(p.notes, "secrets stored on this device: "+strings.Join(sortedKeys(b.set), ", "))
	}
	if len(b.empty) > 0 {
		p.notes = append(p.notes, "secret values left empty: "+strings.Join(b.empty, ", ")+" (set them in Settings > MCP, or use "+secretsHint+")")
	}
	if len(b.unset) > 0 {
		p.notes = append(p.notes, "the tool filled in "+strings.Join(b.unset, ", ")+" itself; set them in Settings > MCP")
	}
	return p
}

// queryURL moves secret-looking query values into secret references.
func (b *secretBook) queryURL(u string) string {
	base, q, ok := strings.Cut(u, "?")
	if !ok {
		return u
	}
	frag := ""
	if i := strings.IndexByte(q, '#'); i >= 0 {
		q, frag = q[:i], q[i:]
	}
	parts := strings.Split(q, "&")
	for i, kv := range parts {
		k, v, ok := strings.Cut(kv, "=")
		if ok && v != "" && !strings.Contains(v, "${secret:") && isSecret(k, v) {
			parts[i] = k + "=" + b.secret(k, v)
		}
	}
	return base + "?" + strings.Join(parts, "&") + frag
}

// summary is what would run or be contacted, for the note. A URL loses its query: it may hold a token.
func (p mcpPlan) summary() string {
	s := p.server
	if s == nil {
		return ""
	}
	switch s.GetTransport() {
	case mcpv1.Transport_TRANSPORT_STDIO:
		line := strings.TrimSpace(s.GetStdio().GetCommand() + " " + strings.Join(s.GetStdio().GetArgs(), " "))
		if len(line) > 80 {
			line = line[:77] + "..."
		}
		return "stdio " + line
	case mcpv1.Transport_TRANSPORT_SSE:
		return "sse " + stripQuery(s.GetRemote().GetUrl())
	}
	return "http " + stripQuery(s.GetRemote().GetUrl())
}

func stripQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mcpUser plans or creates one user-level server. existing is by name; nil target: listed for manual setup.
func mcpUser(ctx context.Context, target MCPTarget, existing map[string]ExistingMCP, name string, m *MCPServer, apply, include, force bool, home, forceHint, secretsHint string) (Status, string, bool) {
	p := planMCP(name, m, include, home, secretsHint)
	if p.manual != "" {
		return StatusNoted, p.manual, false
	}
	notes := append([]string{p.summary()}, p.notes...)
	if target == nil {
		return StatusNoted, joinNotes(append(notes, "add it in Settings > MCP")), p.hasSecrets
	}
	cur, have := existing[p.server.GetName()]
	if have && (!force || !cur.CanEdit) {
		why := "already there"
		if cur.ManagedBy != "" {
			why += " (" + cur.ManagedBy + ")"
		}
		if !cur.CanEdit {
			why += "; it can't be changed from here"
		} else {
			why += "; use " + forceHint + " to replace it"
		}
		return StatusExists, joinNotes(append(notes, why)), p.hasSecrets
	}
	if !apply {
		if have {
			notes = append(notes, "replaces the one already there")
		}
		return StatusPlanned, joinNotes(notes), p.hasSecrets
	}
	var err error
	if have {
		err = target.Replace(ctx, cur.ID, p.server, p.secrets)
		notes = append(notes, "replaced the one already there")
	} else {
		err = target.Create(ctx, p.server, p.secrets)
	}
	if err != nil {
		return StatusError, joinNotes(append(notes, err.Error())), p.hasSecrets
	}
	existing[p.server.GetName()] = ExistingMCP{Name: p.server.GetName(), ManagedBy: "This device", CanEdit: true}
	return StatusUploaded, joinNotes(append(notes, "added on this device")), p.hasSecrets
}

// existingMCP lists the target's servers by name.
func existingMCP(ctx context.Context, target MCPTarget) (map[string]ExistingMCP, error) {
	out := map[string]ExistingMCP{}
	if target == nil {
		return out, nil
	}
	list, err := target.Servers(ctx)
	if err != nil {
		return out, err
	}
	for _, s := range list {
		if _, dup := out[s.Name]; !dup || s.CanEdit {
			out[s.Name] = s
		}
	}
	return out, nil
}

// mcpProjectEntry is the folder form of a server, in .mcp.json's shape. Secret values stay out unless include;
// variables stay as the tool wrote them, in ${NAME} form.
func mcpProjectEntry(m *MCPServer, include bool) (map[string]any, []string, bool, error) {
	var left []string
	plain := func(k, v string) string {
		v = varRefRe.ReplaceAllStringFunc(v, func(s string) string {
			g := varRefRe.FindStringSubmatch(s)
			return "${" + g[2] + g[3] + "}"
		})
		if !strings.Contains(v, "${") && v != "" && isSecret(k, v) {
			if !include {
				left = append(left, k)
				return ""
			}
		}
		return v
	}
	e := map[string]any{}
	switch t := m.transport(); t {
	case "stdio":
		if m.Command == "" {
			return nil, nil, false, errors.New("no command to run")
		}
		e["command"] = m.Command
		if len(m.Args) > 0 {
			args := make([]any, 0, len(m.Args))
			for i, a := range m.Args {
				if secretValue.MatchString(strings.TrimSpace(a)) && !include {
					left = append(left, fmt.Sprintf("argument %d", i+1))
					a = ""
				}
				args = append(args, a)
			}
			e["args"] = args
		}
		if len(m.Env) > 0 {
			env := map[string]any{}
			for k, v := range m.Env {
				env[k] = plain(k, v)
			}
			e["env"] = env
		}
	case "http", "sse", "ws":
		if m.URL == "" {
			return nil, nil, false, errors.New("no URL")
		}
		e["type"] = t
		e["url"] = m.URL
		if len(m.Headers) > 0 {
			h := map[string]any{}
			for k, v := range m.Headers {
				h[k] = plain(k, v)
			}
			e["headers"] = h
		}
	default:
		return nil, nil, false, fmt.Errorf("transport %s is not one harness speaks", t)
	}
	sort.Strings(left)
	return e, left, len(left) > 0 || (include && hasSecretValues(m)), nil
}

func hasSecretValues(m *MCPServer) bool {
	for k, v := range m.Env {
		if isSecret(k, v) {
			return true
		}
	}
	for k, v := range m.Headers {
		if isSecret(k, v) {
			return true
		}
	}
	return false
}

// ParseMCPServers reads the common `{"<key>": {"name": {...}}}` shape (key "mcpServers" for Claude, Cursor, Gemini,
// Cline, Roo, Kiro, Windsurf; "servers" for VS Code). Field spellings of the tools are accepted: type/transport,
// url/serverUrl/httpUrl (httpUrl is Streamable HTTP), disabled/enabled. A nil value is an entry that does not parse.
func ParseMCPServers(data []byte, key string) (map[string]*MCPServer, error) {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	raw, ok := f[key]
	if !ok {
		return map[string]*MCPServer{}, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	out := map[string]*MCPServer{}
	for name, e := range entries {
		var x struct {
			Type      string            `json:"type"`
			Transport string            `json:"transport"`
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			Cwd       string            `json:"cwd"`
			URL       string            `json:"url"`
			ServerURL string            `json:"serverUrl"`
			HTTPURL   string            `json:"httpUrl"`
			Headers   map[string]string `json:"headers"`
			Disabled  bool              `json:"disabled"`
			Enabled   *bool             `json:"enabled"`
		}
		if err := json.Unmarshal(e, &x); err != nil {
			out[name] = nil // malformed entry: the reader reports it
			continue
		}
		m := &MCPServer{Transport: firstNonEmpty(x.Type, x.Transport), Command: x.Command, Args: x.Args, Env: x.Env, Cwd: x.Cwd,
			URL: firstNonEmpty(x.URL, x.ServerURL), Headers: x.Headers, Disabled: x.Disabled || (x.Enabled != nil && !*x.Enabled)}
		// A bare url is Streamable HTTP for most tools; a reader whose tool means SSE by it (Gemini) sets Transport.
		if x.HTTPURL != "" {
			m.URL, m.Transport = x.HTTPURL, "http"
		}
		out[name] = m
	}
	return out, nil
}
