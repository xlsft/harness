package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpv1 "github.com/xlsft/harness/packages/client/gen/harness/mcp/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// The names of the agent's resource tools (prompt bundle entries).
const (
	ListResourcesToolName = "ListMcpResources"
	ReadResourceToolName  = "ReadMcpResource"
)

const (
	// listCacheTTL keeps prompt and resource lists between composer key presses; list_changed clears them sooner.
	listCacheTTL = time.Minute
	// listTimeout bounds asking one server for a list.
	listTimeout = 5 * time.Second
	// maxListPages bounds proxied list pagination.
	maxListPages = 20
)

type cachedList[T any] struct {
	items []T
	at    time.Time
}

func (m *Manager) invalidateLists(s *server, kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch kind {
	case "prompts":
		delete(m.promptCache, s.cfg.GetId())
	case "resources":
		delete(m.resourceCache, s.cfg.GetId())
	}
}

// usable reports whether the server may be contacted for prompts and resources now.
func (m *Manager) usable(s *server) (*directConn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.needsApproval && s.approval != mcpv1.ApprovalState_APPROVAL_STATE_APPROVED {
		return nil, false
	}
	if s.remote {
		return nil, s.cfg.GetAuth().GetState() != mcpv1.AuthState_AUTH_STATE_NEEDS_AUTH
	}
	return s.conn, s.conn != nil
}

func (m *Manager) findServer(name string) *server {
	for _, s := range m.servers {
		if s.name() == name || NormalizeName(s.name()) == name {
			return s
		}
	}
	return nil
}

// promptsOf lists a server's prompts; a server that cannot be asked has none.
func (m *Manager) promptsOf(ctx context.Context, s *server) []*mcpv1.Prompt {
	// A deny rule on the whole server covers its prompts, like its resources; mcp__<server>__get_prompt names them alone.
	if m.disabled(ToolName(s.name(), "get_prompt")) {
		return nil
	}
	id := s.cfg.GetId()
	m.mu.Lock()
	c, ok := m.promptCache[id]
	m.mu.Unlock()
	if ok && time.Since(c.at) < listCacheTTL {
		return c.items
	}
	conn, can := m.usable(s)
	if !can {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var list []*mcpv1.Prompt
	var err error
	if s.remote {
		list, err = m.o.Client.AllPrompts(ctx, id)
	} else {
		list, err = conn.listPrompts(ctx)
	}
	if err != nil {
		m.log.Debug("MCP prompts unavailable", "server", s.name(), "err", err)
		return nil
	}
	m.mu.Lock()
	m.promptCache[id] = cachedList[*mcpv1.Prompt]{items: list, at: time.Now()}
	m.mu.Unlock()
	return list
}

// resourcesOf lists a server's resources; a server that cannot be asked has none.
func (m *Manager) resourcesOf(ctx context.Context, s *server) ([]*mcpv1.Resource, error) {
	id := s.cfg.GetId()
	m.mu.Lock()
	c, ok := m.resourceCache[id]
	m.mu.Unlock()
	if ok && time.Since(c.at) < listCacheTTL {
		return c.items, nil
	}
	conn, can := m.usable(s)
	if !can {
		return nil, fmt.Errorf("MCP server %s is not connected", s.name())
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var list []*mcpv1.Resource
	var err error
	if s.remote {
		list, _, err = m.o.Client.AllResources(ctx, id)
	} else {
		list, _, err = conn.listResources(ctx)
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.resourceCache[id] = cachedList[*mcpv1.Resource]{items: list, at: time.Now()}
	m.mu.Unlock()
	return list, nil
}

// PromptCommand is one prompt as a slash command.
type PromptCommand struct {
	Command, Server, Prompt, Title, Description string
	Arguments                                   []PromptArgument
}

// PromptArgument is one argument of a prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

func parsePromptArguments(raw string) []PromptArgument {
	var out []PromptArgument
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// CommandName returns a prompt's slash command.
func CommandName(server, prompt string) string { return "/" + ToolName(server, prompt) }

// Commands lists the prompts of the session's servers as slash commands, sorted, asking servers concurrently.
func (m *Manager) Commands(ctx context.Context) []PromptCommand {
	if m == nil {
		return nil
	}
	var mu sync.Mutex
	var out []PromptCommand
	var wg sync.WaitGroup
	for _, s := range m.servers {
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			for _, p := range m.promptsOf(ctx, s) {
				c := PromptCommand{Command: CommandName(s.name(), p.GetName()), Server: s.name(), Prompt: p.GetName(), Title: p.GetTitle(),
					Description: p.GetDescription(), Arguments: parsePromptArguments(p.GetArgumentsJson())}
				mu.Lock()
				out = append(out, c)
				mu.Unlock()
			}
		}(s)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

// ResourceMention is one resource, for @-mentions.
type ResourceMention struct {
	Mention, Server, URI, Name, Description, MimeType string
}

// Mentions lists the resources of the session's servers. A query filters by substring.
func (m *Manager) Mentions(ctx context.Context, query string, limit int) []ResourceMention {
	if m == nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	query = strings.ToLower(query)
	var mu sync.Mutex
	var out []ResourceMention
	var wg sync.WaitGroup
	for _, s := range m.servers {
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			list, err := m.resourcesOf(ctx, s)
			if err != nil {
				return
			}
			for _, r := range list {
				x := ResourceMention{Mention: "@" + s.name() + ":" + r.GetUri(), Server: s.name(), URI: r.GetUri(), Name: r.GetName(), Description: r.GetDescription(), MimeType: r.GetMimeType()}
				if query != "" && !strings.Contains(strings.ToLower(x.Mention+" "+x.Name), query) {
					continue
				}
				mu.Lock()
				out = append(out, x)
				mu.Unlock()
			}
		}(s)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Mention < out[j].Mention })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ParseCommand splits "/mcp__server__prompt args". ok false: the text is not an MCP command.
func ParseCommand(text string) (name, args string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/"+Prefix) {
		return "", "", false
	}
	name, args, _ = strings.Cut(text, " ")
	if i := strings.IndexAny(text, " \t\n"); i >= 0 {
		name, args = text[:i], strings.TrimSpace(text[i+1:])
	}
	return name, args, true
}

// bindArguments maps a command's arguments: name=value words, the rest by position (the last argument takes what is
// left of the line).
func bindArguments(defs []PromptArgument, line string) (map[string]string, error) {
	out := map[string]string{}
	var free []string
	byName := map[string]bool{}
	for _, d := range defs {
		byName[d.Name] = true
	}
	for _, w := range strings.Fields(line) {
		if k, v, ok := strings.Cut(w, "="); ok && byName[k] {
			out[k] = v
			continue
		}
		free = append(free, w)
	}
	var open []string
	for _, d := range defs {
		if _, set := out[d.Name]; !set {
			open = append(open, d.Name)
		}
	}
	for i, name := range open {
		if i >= len(free) {
			break
		}
		if i == len(open)-1 {
			out[name] = strings.Join(free[i:], " ")
			free = nil
			break
		}
		out[name] = free[i]
	}
	var missing []string
	for _, d := range defs {
		if _, set := out[d.Name]; !set && d.Required {
			missing = append(missing, d.Name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing argument: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

type promptResult struct {
	Messages []struct {
		Role    string      `json:"role"`
		Content contentItem `json:"content"`
	} `json:"messages"`
}

// ExpandCommand fetches the prompt a "/mcp__..." command names and returns its text as message content. handled false:
// the name is no prompt of this session and the text goes to the model as it is.
func (m *Manager) ExpandCommand(ctx context.Context, name, argLine string) (blocks []llm.ContentBlock, handled bool, err error) {
	if m == nil {
		return nil, false, nil
	}
	var cmd *PromptCommand
	for _, c := range m.Commands(ctx) {
		if c.Command == name {
			cmd = &c
			break
		}
	}
	if cmd == nil {
		return nil, false, nil
	}
	args, err := bindArguments(cmd.Arguments, argLine)
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", name, err)
	}
	s := m.findServer(cmd.Server)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var raw []byte
	if s.remote {
		r, err := m.o.Client.GetPrompt(ctx, s.cfg.GetId(), cmd.Prompt, args)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %s", name, errorMessage(err))
		}
		raw = []byte(r)
	} else {
		conn, ok := m.usable(s)
		if !ok {
			return nil, true, fmt.Errorf("MCP server %s is not running", s.name())
		}
		res, err := conn.session.GetPrompt(ctx, &sdk.GetPromptParams{Name: cmd.Prompt, Arguments: args})
		if err != nil {
			return nil, true, fmt.Errorf("%s: %w", name, err)
		}
		raw, _ = json.Marshal(res)
	}
	var pr promptResult
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, true, fmt.Errorf("%s: invalid prompt: %w", name, err)
	}
	for _, msg := range pr.Messages {
		c := msg.Content
		switch {
		case c.Type == "text" && msg.Role == "assistant":
			blocks = append(blocks, llm.TextBlock("[The prompt's assistant message]\n"+c.Text))
		case c.Type == "text":
			blocks = append(blocks, llm.TextBlock(c.Text))
		case c.Type == "image" && imageMediaTypes[c.MimeType] && len(c.Data) <= maxImageBytes:
			blocks = append(blocks, llm.ContentBlock{Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: c.MimeType, Data: c.Data}})
		case c.Type == "resource" && c.Resource != nil && c.Resource.Text != "":
			blocks = append(blocks, llm.TextBlock(c.Resource.Text))
		}
	}
	if len(blocks) == 0 {
		return nil, true, fmt.Errorf("%s: the prompt has no content", name)
	}
	return blocks, true, nil
}

// mentionRe finds @server:uri words, at the start of the text or after whitespace.
var mentionRe = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9][A-Za-z0-9_.-]*):(\S+)`)

// maxMentionChars caps the text one mention attaches.
const maxMentionChars = 50_000

// ExpandMentions returns content blocks with the resources a text mentions, to add to the message. Words that name no
// server of this session (a file path such as @src:main) stay plain text.
func (m *Manager) ExpandMentions(ctx context.Context, text string) []llm.ContentBlock {
	if m == nil || !strings.Contains(text, "@") {
		return nil
	}
	var out []llm.ContentBlock
	for _, match := range mentionRe.FindAllStringSubmatch(text, 8) {
		s := m.findServer(match[1])
		if s == nil {
			continue
		}
		uri := strings.TrimRight(match[2], ".,;:!?)")
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		raw, err := m.readResource(ctx, s, uri)
		cancel()
		head := fmt.Sprintf("<mcp-resource server=%q uri=%q>\n", s.name(), uri)
		if err != nil {
			out = append(out, llm.TextBlock(head+"The resource could not be read: "+err.Error()+"\n</mcp-resource>"))
			continue
		}
		res := resourceBlocks(raw, maxMentionChars)
		if len(res) > 0 && res[0].Type == llm.BlockText {
			res[0].Text = head + res[0].Text
		} else {
			res = append([]llm.ContentBlock{llm.TextBlock(head)}, res...)
		}
		out = append(out, res...)
		out = append(out, llm.TextBlock("</mcp-resource>"))
	}
	return out
}

func (m *Manager) readResource(ctx context.Context, s *server, uri string) ([]byte, error) {
	if m.disabled(ToolName(s.name(), "read_resource")) {
		return nil, fmt.Errorf("a permission rule denies MCP server %s", s.name())
	}
	conn, can := m.usable(s)
	if !can {
		return nil, fmt.Errorf("MCP server %s is not connected", s.name())
	}
	if s.remote {
		r, err := m.o.Client.ReadResource(ctx, s.cfg.GetId(), uri)
		if err != nil {
			return nil, errors.New(errorMessage(err))
		}
		return []byte(r), nil
	}
	res, err := conn.session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

// resourceBlocks maps an MCP ReadResourceResult to content blocks.
func resourceBlocks(raw []byte, limit int) []llm.ContentBlock {
	var r struct {
		Contents []struct {
			URI      string `json:"uri"`
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return []llm.ContentBlock{llm.TextBlock("The server returned an invalid resource.")}
	}
	var out []llm.ContentBlock
	left := limit
	for _, c := range r.Contents {
		switch {
		case c.Blob != "" && imageMediaTypes[c.MimeType] && len(c.Blob) <= maxImageBytes:
			out = append(out, llm.ContentBlock{Type: llm.BlockImage, Source: &llm.Source{Type: "base64", MediaType: c.MimeType, Data: c.Blob}})
		case c.Blob != "":
			out = append(out, llm.TextBlock(fmt.Sprintf("[binary resource omitted: %s %s, %d bytes of base64]", c.URI, c.MimeType, len(c.Blob))))
		case left > 0:
			text := c.Text
			if len(text) > left {
				text = truncate(text, left) + fmt.Sprintf("\n[truncated: the resource exceeded %d characters]", limit)
			}
			left -= len(text)
			out = append(out, llm.TextBlock(text))
		}
	}
	if len(out) == 0 {
		out = append(out, llm.TextBlock("(empty resource)"))
	}
	return out
}

// syncResourceTools makes the resource tools exist once a server says it has resources and the bundle defines them.
// It takes the locks it needs, so call it holding none.
func (m *Manager) syncResourceTools() {
	if m.o.Bundle == nil {
		return
	}
	_, a := m.o.Bundle.Tools[ListResourcesToolName]
	_, b := m.o.Bundle.Tools[ReadResourceToolName]
	if !a || !b {
		return
	}
	found := false
	for _, s := range m.servers {
		s.mu.Lock()
		if s.conn != nil && s.conn.hasResources() {
			found = true
		}
		s.mu.Unlock()
		if s.remote {
			m.mu.Lock()
			found = found || m.remoteInfo[s.cfg.GetId()].GetHasResources()
			m.mu.Unlock()
		}
	}
	if !found {
		return
	}
	m.mu.Lock()
	first := !m.resourceTools
	m.resourceTools = true
	m.mu.Unlock()
	if first && m.o.Registry != nil {
		m.o.Registry.Register(&listResourcesTool{m: m})
		m.o.Registry.Register(&readResourceTool{m: m})
	}
}

// listResourcesTool is the agent's ListMcpResources.
type listResourcesTool struct{ m *Manager }

func (t *listResourcesTool) Name() string { return ListResourcesToolName }

func (t *listResourcesTool) ReadOnly(json.RawMessage) bool { return true }

func (t *listResourcesTool) Run(ctx context.Context, _ *tools.Env, call tools.Call) tools.Result {
	var in struct {
		Server string `json:"server"`
	}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return tools.ErrorResult("InputValidationError: %v", err)
		}
	}
	type item struct {
		Server      string `json:"server"`
		URI         string `json:"uri,omitempty"`
		URITemplate string `json:"uriTemplate,omitempty"`
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
		MimeType    string `json:"mimeType,omitempty"`
	}
	var items []item
	var problems []string
	found := false
	for _, s := range t.m.servers {
		if in.Server != "" && s.name() != in.Server {
			continue
		}
		found = true
		if t.m.disabled(ToolName(s.name(), "read_resource")) {
			continue
		}
		conn, can := t.m.usable(s)
		if !can {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, listTimeout)
		var res []*mcpv1.Resource
		var tpl []*mcpv1.ResourceTemplate
		var err error
		if s.remote {
			res, tpl, err = t.m.o.Client.AllResources(lctx, s.cfg.GetId())
		} else {
			res, tpl, err = conn.listResources(lctx)
		}
		cancel()
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s", s.name(), errorMessage(err)))
			continue
		}
		for _, r := range res {
			items = append(items, item{Server: s.name(), URI: r.GetUri(), Name: r.GetName(), Description: r.GetDescription(), MimeType: r.GetMimeType()})
		}
		for _, r := range tpl {
			items = append(items, item{Server: s.name(), URITemplate: r.GetUriTemplate(), Name: r.GetName(), Description: r.GetDescription(), MimeType: r.GetMimeType()})
		}
	}
	if in.Server != "" && !found {
		return tools.ErrorResult("There is no MCP server named %q in this session.", in.Server)
	}
	body, _ := json.MarshalIndent(items, "", "  ")
	text := string(body)
	if len(items) == 0 {
		text = "No resources found."
	}
	if len(problems) > 0 {
		text += "\n\nNot listed:\n" + strings.Join(problems, "\n")
	}
	r := tools.TextResult(text, map[string]any{"resources": items})
	r.Summary = fmt.Sprintf("%d resource(s)", len(items))
	return r
}

// readResourceTool is the agent's ReadMcpResource.
type readResourceTool struct{ m *Manager }

func (t *readResourceTool) Name() string { return ReadResourceToolName }

func (t *readResourceTool) ReadOnly(json.RawMessage) bool { return true }

func (t *readResourceTool) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	var in struct {
		Server string `json:"server"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	if in.Server == "" || in.URI == "" {
		return tools.ErrorResult("InputValidationError: server and uri are required")
	}
	idx := slices.IndexFunc(t.m.servers, func(s *server) bool { return s.name() == in.Server })
	if idx < 0 {
		return tools.ErrorResult("There is no MCP server named %q in this session.", in.Server)
	}
	s := t.m.servers[idx]
	raw, err := t.m.readResource(ctx, s, in.URI)
	if err != nil {
		if ctx.Err() != nil {
			return tools.ErrorResult("The call was cancelled.")
		}
		return tools.ErrorResult("MCP server %s failed: %v", s.name(), err)
	}
	limit := t.m.o.MaxOutputChars
	if n := int(s.cfg.GetMaxOutputChars()); n > 0 {
		limit = n
	}
	blocks := resourceBlocks(raw, limit)
	var texts []string
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			texts = append(texts, b.Text)
		}
	}
	return tools.Result{Content: blocks, Structured: strings.Join(texts, "\n"), Summary: in.URI}
}
