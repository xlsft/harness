// Package importer is the only reader of Claude Code's files; the rest of the client never looks at them.
//
// `harness-client import claude` is a one-time import of the user-level Claude Code configuration (~/.claude) into the
// harness server, and optionally of Claude Code session transcripts into ~/.harness/projects with upload to the server.
// `harness-client import --project` brings a folder's files into .harness and AGENTS.md (project.go).
//
// Imported at user level:
//
//	~/.claude/CLAUDE.md              → MEMORY (user, key "user"), its @imports below ~/.claude taken in
//	~/.claude/rules/**/*.md          → RULE (key: relative path)
//	~/.claude/settings.json          → SETTINGS (user, key "user"), minus permission rules and hooks
//	  permissions.allow/ask/deny     → PERMISSION_RULES (user, key "user")
//	  hooks                          → HOOK (user, key "user")
//	~/.claude/skills/<name>/         → SKILL (key name; SKILL.md + files)
//	~/.claude/agents/*.md            → AGENT (key: frontmatter name or file name)
//	~/.claude/commands/**/*.md       → COMMAND (key: relative path, "/" → ":")
//	~/.claude/output-styles/*.md     → OUTPUT_STYLE (key: frontmatter name or file name)
//	~/.claude/workflows/*            → copied to ~/.harness/workflows (this device)
//	~/.claude/hooks/**               → copied to ~/.harness/hooks (this device); the hooks' commands are rewritten to match
//	~/.claude.json mcpServers        → reported only (McpService arrives in M2)
//	~/.claude/projects/**/*.jsonl    → copied to ~/.harness/projects and queued for upload (--sessions)
//
// Env values that look like secrets are never uploaded without
// IncludeSecrets.
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"google.golang.org/protobuf/proto"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/memory"
)

// Source label of imported documents.
const Source = "import:claude"

// Limits for skill directories.
const (
	MaxSkillFileBytes  = 1 << 20
	MaxSkillTotalBytes = 8 << 20
	batchSize          = 500
)

// Putter uploads documents (implemented by *syncclient.Client).
type Putter interface {
	BatchPutDocuments(ctx context.Context, req *syncv1.BatchPutDocumentsRequest) (*syncv1.BatchPutDocumentsResponse, error)
}

// SessionQueue receives copied session transcripts (see syncclient.QueueFile
// and Uploader.Flush).
type SessionQueue interface {
	// Queue registers the transcript for upload.
	Queue(sessionID, path string) error
	// Flush uploads it now; errors leave it queued.
	Flush(ctx context.Context, sessionID string) error
}

// Options configure Run.
type Options struct {
	// ClaudeHome is ~/.claude; ClaudeJSON is ~/.claude.json.
	ClaudeHome string
	ClaudeJSON string
	// ProjectsDir is the harness transcript root (~/.harness/projects).
	ProjectsDir string
	// HarnessHome is ~/.harness, for what has no document and stays on this device; empty: those are not imported.
	HarnessHome string

	DryRun         bool
	Sessions       bool
	IncludeSecrets bool
	// Force overwrites existing server documents (unconditional writes);
	// otherwise documents are created only when absent.
	Force bool

	// Putter is required unless DryRun.
	Putter Putter
	// Queue receives copied sessions; nil only copies them.
	Queue SessionQueue
	// UploadSessions flushes queued sessions immediately.
	UploadSessions bool

	// Sections limits the configuration imported; nil: all of it.
	Sections map[Section]bool
	// SelectSession limits Sessions to the transcripts it accepts, by project folder and session id; nil: all.
	// Subagent transcripts travel with their session.
	SelectSession func(folder, id string) bool
	// HoldSession says why a session must stay as it is (open here, of another machine); "": it may be imported.
	HoldSession func(id string) string
	// SessionDone says the server has all of a session's local copy: when that copy is current too, the session is
	// reported as it stands, without going through the queue. nil: always through the queue.
	SessionDone func(id string) bool
	// OnProgress follows a run that uploads (never a dry run); it is called on Run's goroutine.
	OnProgress func(Progress)
	// ForceHint and SecretsHint name the switches in notes; defaults: the CLI's flags.
	ForceHint, SecretsHint string
}

// Section is a choosable part of the configuration.
type Section string

const (
	SectionMemory       Section = "memory"
	SectionSettings     Section = "settings" // with permission rules and hooks
	SectionSkills       Section = "skills"
	SectionAgents       Section = "agents"
	SectionCommands     Section = "commands"
	SectionOutputStyles Section = "output-styles"
	SectionMCP          Section = "mcp"
	SectionWorkflows    Section = "workflows"
)

// SectionOf returns the section of an item kind ("" for a session).
func SectionOf(kind string) Section {
	switch kind {
	case "memory", "rule":
		return SectionMemory
	case "settings", "permissions", "hooks", "hook-script":
		return SectionSettings
	case "workflow":
		return SectionWorkflows
	case "skill":
		return SectionSkills
	case "agent":
		return SectionAgents
	case "command":
		return SectionCommands
	case "output-style":
		return SectionOutputStyles
	case "mcp":
		return SectionMCP
	}
	return ""
}

// Progress is one step of a run: the item Kind/Key started, or, with Item, finished.
type Progress struct {
	Kind, Key   string
	Done, Total int
	Item        *Item
}

// Status of an imported item.
type Status string

const (
	StatusUploaded Status = "uploaded"
	StatusExists   Status = "exists"
	StatusPlanned  Status = "dry-run"
	StatusSkipped  Status = "skipped"
	StatusError    Status = "error"
	StatusCopied   Status = "copied"
	StatusQueued   Status = "queued"
	StatusNoted    Status = "noted"
)

// Item is one line of the report.
type Item struct {
	Kind   string
	Key    string
	Status Status
	Note   string

	req *syncv1.PutDocumentRequest
}

// Report is the import result.
type Report struct {
	Items []Item
}

// Counts returns the number of items per status.
func (r *Report) Counts() map[Status]int {
	m := map[Status]int{}
	for _, it := range r.Items {
		m[it.Status]++
	}
	return m
}

// HasErrors reports whether any item failed.
func (r *Report) HasErrors() bool { return r.Counts()[StatusError] > 0 }

// Print writes the summary table.
func (r *Report) Print(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tKEY\tSTATUS\tNOTE")
	for _, it := range r.Items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.Kind, it.Key, it.Status, it.Note)
	}
	tw.Flush()
	c := r.Counts()
	var parts []string
	for _, s := range []Status{StatusUploaded, StatusExists, StatusPlanned, StatusCopied, StatusQueued, StatusNoted, StatusSkipped, StatusError} {
		if c[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c[s], s))
		}
	}
	if len(parts) == 0 {
		parts = []string{"nothing to import"}
	}
	fmt.Fprintln(w, "\n"+strings.Join(parts, ", "))
}

type run struct {
	o           Options
	rep         *Report
	done, total int
}

func (r *run) wants(s Section) bool { return r.o.Sections == nil || r.o.Sections[s] }

func (r *run) started(kind, key string) {
	if r.o.OnProgress != nil {
		r.o.OnProgress(Progress{Kind: kind, Key: key, Done: r.done, Total: r.total})
	}
}

func (r *run) finished(it Item) {
	r.done++
	r.report(it)
}

func (r *run) report(it Item) {
	if r.o.OnProgress != nil {
		r.o.OnProgress(Progress{Kind: it.Kind, Key: it.Key, Done: r.done, Total: r.total, Item: &it})
	}
}

func (r *run) add(it Item) { r.rep.Items = append(r.rep.Items, it) }

func (r *run) doc(kind syncv1.DocumentKind, label, key, content string, files []*syncv1.DocumentFile, note string) {
	req := &syncv1.PutDocumentRequest{
		Ref:     &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: kind, Key: key},
		Content: content, Files: files, Source: Source,
	}
	if !r.o.Force {
		req.BaseVersion = proto.Int64(0)
	}
	r.add(Item{Kind: label, Key: key, Status: StatusPlanned, Note: note, req: req})
}

// Run performs the import.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.ClaudeHome == "" {
		return nil, fmt.Errorf("importer: ClaudeHome is required")
	}
	if !o.DryRun && o.Putter == nil {
		return nil, fmt.Errorf("importer: a server connection is required (or use --dry-run)")
	}
	if o.ForceHint == "" {
		o.ForceHint = "--force"
	}
	if o.SecretsHint == "" {
		o.SecretsHint = "--include-secrets"
	}
	r := &run{o: o, rep: &Report{}}
	if r.wants(SectionMemory) {
		r.memory()
		r.rules()
	}
	if r.wants(SectionSettings) {
		r.settings()
		r.deviceFiles("hook-script", "hooks")
	}
	if r.wants(SectionWorkflows) {
		r.deviceFiles("workflow", "workflows")
	}
	if r.wants(SectionSkills) {
		r.skills()
	}
	if r.wants(SectionAgents) {
		r.markdownDir("agent", syncv1.DocumentKind_DOCUMENT_KIND_AGENT, filepath.Join(o.ClaudeHome, "agents"), false)
	}
	if r.wants(SectionCommands) {
		r.markdownDir("command", syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, filepath.Join(o.ClaudeHome, "commands"), true)
	}
	if r.wants(SectionOutputStyles) {
		r.markdownDir("output-style", syncv1.DocumentKind_DOCUMENT_KIND_OUTPUT_STYLE, filepath.Join(o.ClaudeHome, "output-styles"), false)
	}
	if r.wants(SectionMCP) {
		r.mcp()
	}
	var units []sessionUnit
	if o.Sessions {
		units = r.sessionUnits()
	}
	r.total = len(r.rep.Items) + len(units)
	if !o.DryRun {
		for _, it := range r.rep.Items {
			if it.req == nil {
				r.finished(it)
			}
		}
		if err := r.upload(ctx); err != nil {
			return r.rep, err
		}
	}
	if o.Sessions {
		r.sessions(ctx, units)
	}
	return r.rep, ctx.Err()
}

func (r *run) memory() {
	p := filepath.Join(r.o.ClaudeHome, "CLAUDE.md")
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) == "" {
		r.add(Item{Kind: "memory", Key: "user", Status: StatusSkipped, Note: "CLAUDE.md is empty"})
		return
	}
	var notes []string
	content := r.expandImports(string(data), p, map[string]bool{p: true}, 1, &notes)
	r.doc(syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "memory", "user", content, nil, strings.Join(notes, "; "))
}

// expandImports takes in the @imports below ~/.claude, which nothing reads any more. Others stay references: they may be
// private to this machine, and each device resolves them for itself.
func (r *run) expandImports(content, file string, seen map[string]bool, depth int, notes *[]string) string {
	home := filepath.Dir(r.o.ClaudeHome)
	return memory.RewriteImports(content, func(raw string) string {
		keep := "@" + strings.ReplaceAll(raw, " ", `\ `)
		target := memory.ResolveImport(raw, filepath.Dir(file), home)
		if target == "" {
			return keep
		}
		if !withinDir(target, r.o.ClaudeHome) && !memory.IsImportSourceName(filepath.Base(target)) {
			*notes = append(*notes, keep+" stays a reference; the file stays on this device")
			return keep
		}
		if seen[target] || depth > memory.MaxImportDepth {
			return ""
		}
		seen[target] = true
		data, err := os.ReadFile(target)
		if err != nil {
			*notes = append(*notes, keep+" could not be read")
			return ""
		}
		return strings.TrimSpace(r.expandImports(string(data), target, seen, depth+1, notes))
	})
}

// rules imports ~/.claude/rules/**/*.md, keyed by relative path.
func (r *run) rules() {
	root := filepath.Join(r.o.ClaudeHome, "rules")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		key := filepath.ToSlash(rel)
		if strings.TrimSpace(string(data)) == "" {
			r.add(Item{Kind: "rule", Key: key, Status: StatusSkipped, Note: "empty file"})
			return nil
		}
		r.doc(syncv1.DocumentKind_DOCUMENT_KIND_RULE, "rule", key, string(data), nil, "")
		return nil
	})
}

// deviceFiles copies ~/.claude/<sub> to ~/.harness/<sub>: there is no document for these, and a run never touches what is there.
func (r *run) deviceFiles(kind, sub string) {
	if r.o.HarnessHome == "" {
		return
	}
	root := filepath.Join(r.o.ClaudeHome, sub)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		key, dst := filepath.ToSlash(rel), filepath.Join(r.o.HarnessHome, sub, rel)
		if _, err := os.Lstat(dst); err == nil && !r.o.Force {
			r.add(Item{Kind: kind, Key: key, Status: StatusExists, Note: "already in " + sub})
			return nil
		}
		if r.o.DryRun {
			r.add(Item{Kind: kind, Key: key, Status: StatusPlanned, Note: "copied to this device"})
			return nil
		}
		info, err := d.Info()
		if err == nil {
			var data []byte
			if data, err = os.ReadFile(p); err == nil {
				if err = os.MkdirAll(filepath.Dir(dst), 0o700); err == nil {
					err = os.WriteFile(dst, data, info.Mode().Perm())
				}
			}
		}
		if err != nil {
			r.add(Item{Kind: kind, Key: key, Status: StatusError, Note: err.Error()})
			return nil
		}
		r.add(Item{Kind: kind, Key: key, Status: StatusCopied, Note: "copied to this device"})
		return nil
	})
}

var (
	secretKey   = regexp.MustCompile(`(?i)(key|token|secret|passw|credential|auth|private|cookie|session)`)
	secretValue = regexp.MustCompile(`^(sk-|sk_|ghp_|gho_|ghu_|ghs_|github_pat_|glpat-|xox[abposr]-|AKIA[0-9A-Z]{12,}|AIza|eyJ[A-Za-z0-9_-]{10,}\.)`)
)

// isSecret reports whether an env entry looks like a secret.
func isSecret(k, v string) bool {
	return secretKey.MatchString(k) || secretValue.MatchString(strings.TrimSpace(v))
}

func (r *run) settings() {
	p := filepath.Join(r.o.ClaudeHome, "settings.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		r.add(Item{Kind: "settings", Key: "user", Status: StatusError, Note: "invalid JSON: " + err.Error()})
		return
	}
	var notes []string

	if raw, ok := m["permissions"]; ok {
		var perms map[string]json.RawMessage
		if json.Unmarshal(raw, &perms) == nil {
			rules := map[string]json.RawMessage{}
			for _, k := range []string{"allow", "ask", "deny"} {
				if v, ok := perms[k]; ok {
					rules[k] = v
					delete(perms, k)
				}
			}
			if len(rules) > 0 {
				b, _ := json.MarshalIndent(rules, "", "  ")
				r.doc(syncv1.DocumentKind_DOCUMENT_KIND_PERMISSION_RULES, "permissions", "user", string(b), nil, countRules(rules))
			}
			if len(perms) > 0 {
				// defaultMode, additionalDirectories, ... have no home in
				// PERMISSION_RULES; keep them in the settings document.
				b, _ := json.Marshal(perms)
				m["permissions"] = b
			} else {
				delete(m, "permissions")
			}
		}
	}
	if raw, ok := m["hooks"]; ok {
		delete(m, "hooks")
		note := "runs only after it is trusted on each device"
		rewritten, left := rewriteHooksJSON(raw)
		r.doc(syncv1.DocumentKind_DOCUMENT_KIND_HOOK, "hooks", "user", prettyJSON(rewritten), nil, joinNote(note, left))
	}
	if _, ok := m["apiKeyHelper"]; ok {
		delete(m, "apiKeyHelper")
		notes = append(notes, "apiKeyHelper dropped (keys live on the server)")
	}
	if raw, ok := m["env"]; ok {
		var env map[string]any
		if json.Unmarshal(raw, &env) == nil {
			var dropped []string
			for k, v := range env {
				s, _ := v.(string)
				if isSecret(k, s) && !r.o.IncludeSecrets {
					dropped = append(dropped, k)
					delete(env, k)
				}
			}
			sort.Strings(dropped)
			if len(dropped) > 0 {
				notes = append(notes, "secret env not uploaded: "+strings.Join(dropped, ", ")+" (use "+r.o.SecretsHint+")")
			}
			if len(env) == 0 {
				delete(m, "env")
			} else {
				b, _ := json.Marshal(env)
				m["env"] = b
			}
		}
	}
	if len(m) == 0 {
		if len(notes) > 0 {
			r.add(Item{Kind: "settings", Key: "user", Status: StatusSkipped, Note: strings.Join(notes, "; ")})
		}
		return
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	r.doc(syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, "settings", "user", string(b), nil, strings.Join(notes, "; "))
}

// rewriteHooksJSON points hook commands at the copies in ~/.harness; left notes what still names .claude.
func rewriteHooksJSON(raw json.RawMessage) (json.RawMessage, string) {
	wrapped := map[string]any{}
	var hooks any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&hooks) != nil {
		return raw, ""
	}
	wrapped["hooks"] = hooks
	notes := rewriteCommands(wrapped)
	out, err := json.Marshal(wrapped["hooks"])
	if err != nil {
		return raw, ""
	}
	return out, strings.Join(notes, "; ")
}

func countRules(rules map[string]json.RawMessage) string {
	var parts []string
	for _, k := range []string{"allow", "ask", "deny"} {
		var list []any
		if json.Unmarshal(rules[k], &list) == nil && len(list) > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", len(list), k))
		}
	}
	return strings.Join(parts, ", ")
}

func prettyJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func (r *run) skills() {
	root := filepath.Join(r.o.ClaudeHome, "skills")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if st, err := os.Stat(dir); err != nil || !st.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillMD, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			r.add(Item{Kind: "skill", Key: e.Name(), Status: StatusSkipped, Note: "no SKILL.md"})
			continue
		}
		var files []*syncv1.DocumentFile
		var skipped []string
		total := len(skillMD)
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == dir {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				skipped = append(skipped, rel+" (symlink)")
				return nil
			}
			if d.IsDir() || rel == "SKILL.md" {
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			if info.Size() > MaxSkillFileBytes || total+int(info.Size()) > MaxSkillTotalBytes {
				skipped = append(skipped, rel+" (too large)")
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			total += len(data)
			files = append(files, &syncv1.DocumentFile{Path: rel, Content: data, Executable: info.Mode()&0o111 != 0})
			return nil
		})
		note := ""
		if len(files) > 0 {
			note = fmt.Sprintf("%d extra files", len(files))
		}
		if len(skipped) > 0 {
			if note != "" {
				note += "; "
			}
			note += "skipped " + strings.Join(skipped, ", ")
		}
		r.doc(syncv1.DocumentKind_DOCUMENT_KIND_SKILL, "skill", e.Name(), string(skillMD), files, note)
	}
}

// markdownDir imports *.md files of dir. recursive keys files by their
// relative path with ":" as the namespace separator (commands); otherwise
// only top-level files are read and the frontmatter name wins.
func (r *run) markdownDir(label string, kind syncv1.DocumentKind, dir string, recursive bool) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return
	}
	seen := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (!recursive || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		key := strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
		if recursive {
			key = strings.ReplaceAll(key, "/", ":")
		} else if n := memory.FrontmatterString(string(data), "name"); n != "" {
			key = n
		}
		if prev, dup := seen[key]; dup {
			r.add(Item{Kind: label, Key: key, Status: StatusSkipped, Note: "duplicate of " + prev})
			return nil
		}
		seen[key] = rel
		if strings.TrimSpace(string(data)) == "" {
			r.add(Item{Kind: label, Key: key, Status: StatusSkipped, Note: "empty file"})
			return nil
		}
		r.doc(kind, label, key, string(data), nil, "")
		return nil
	})
}

func (r *run) mcp() {
	if r.o.ClaudeJSON == "" {
		return
	}
	data, err := os.ReadFile(r.o.ClaudeJSON)
	if err != nil {
		return
	}
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			URL     string            `json:"url"`
			Env     map[string]string `json:"env"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		r.add(Item{Kind: "mcp", Key: filepath.Base(r.o.ClaudeJSON), Status: StatusError, Note: "invalid JSON: " + err.Error()})
		return
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for n := range cfg.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := cfg.MCPServers[n]
		typ := s.Type
		if typ == "" {
			typ = "stdio"
			if s.URL != "" {
				typ = "http"
			}
		}
		target := s.Command
		if target == "" {
			target = s.URL
		}
		note := typ
		if target != "" {
			note += " " + target
		}
		if len(s.Env)+len(s.Headers) > 0 {
			note += fmt.Sprintf("; %d env/header values not imported", len(s.Env)+len(s.Headers))
		}
		note += "; add it on the server (MCP import arrives with McpService)"
		r.add(Item{Kind: "mcp", Key: n, Status: StatusNoted, Note: note})
	}
}

func (r *run) upload(ctx context.Context) error {
	var idx []int
	for i, it := range r.rep.Items {
		if it.req != nil && it.Status == StatusPlanned {
			idx = append(idx, i)
		}
	}
	for start := 0; start < len(idx); start += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+batchSize, len(idx))
		chunk := idx[start:end]
		req := &syncv1.BatchPutDocumentsRequest{}
		for _, i := range chunk {
			req.Items = append(req.Items, r.rep.Items[i].req)
		}
		r.started(r.rep.Items[chunk[0]].Kind, r.rep.Items[chunk[0]].Key)
		resp, err := r.o.Putter.BatchPutDocuments(ctx, req)
		if err != nil {
			for _, i := range chunk {
				r.rep.Items[i].Status = StatusError
				r.rep.Items[i].Note = joinNote(r.rep.Items[i].Note, err.Error())
				r.finished(r.rep.Items[i])
			}
			return fmt.Errorf("upload: %w", err)
		}
		for j, i := range chunk {
			it := &r.rep.Items[i]
			if j >= len(resp.GetResults()) {
				it.Status, it.Note = StatusError, joinNote(it.Note, "no result from server")
				r.finished(*it)
				continue
			}
			switch res := resp.GetResults()[j].GetResult().(type) {
			case *syncv1.BatchPutResult_Document:
				it.Status = StatusUploaded
				it.Note = joinNote(it.Note, fmt.Sprintf("v%d", res.Document.GetVersion()))
			case *syncv1.BatchPutResult_Conflict:
				it.Status = StatusExists
				it.Note = joinNote(it.Note, "already on the server (use "+r.o.ForceHint+" to overwrite)")
			case *syncv1.BatchPutResult_Error:
				it.Status = StatusError
				it.Note = joinNote(it.Note, res.Error.GetCode()+": "+res.Error.GetMessage())
			default:
				it.Status, it.Note = StatusError, joinNote(it.Note, "empty result")
			}
			r.finished(*it)
		}
	}
	return nil
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

var sessionFile = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\.jsonl$`)

// sessionUnit is what travels together: a session's transcript with those of its subagents, or a stray transcript.
type sessionUnit struct {
	key, path, folder, id string
	// top says it is a session: <folder>/<uuid>.jsonl.
	top bool
	// extras are the subagent transcripts, relative to the source root.
	extras []string
}

// sessionUnits lists the selected transcripts under ClaudeHome/projects.
func (r *run) sessionUnits() []sessionUnit {
	src := filepath.Join(r.o.ClaudeHome, "projects")
	if r.o.ProjectsDir == "" {
		r.add(Item{Kind: "session", Key: src, Status: StatusError, Note: "no harness projects directory configured"})
		return nil
	}
	var files []string
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	// Sorted, "<uuid>.jsonl" comes before "<uuid>/...", so a session is known before its subagents.
	var units []sessionUnit
	byID := map[string]int{}
	for _, p := range files {
		rel, _ := filepath.Rel(src, p)
		key := filepath.ToSlash(rel)
		parts := strings.Split(key, "/")
		if len(parts) < 2 {
			continue
		}
		id := strings.TrimSuffix(parts[len(parts)-1], ".jsonl")
		if len(parts) > 2 {
			id = parts[1]
			if i, ok := byID[parts[0]+"/"+id]; ok {
				units[i].extras = append(units[i].extras, rel)
				continue
			}
		}
		if r.o.SelectSession != nil && !r.o.SelectSession(parts[0], id) {
			continue
		}
		top := len(parts) == 2 && sessionFile.MatchString(parts[1])
		if top {
			byID[parts[0]+"/"+id] = len(units)
		}
		units = append(units, sessionUnit{key: key, path: rel, folder: parts[0], id: id, top: top})
	}
	return units
}

func (r *run) sessions(ctx context.Context, units []sessionUnit) {
	src := filepath.Join(r.o.ClaudeHome, "projects")
	var subagents int
	for _, u := range units {
		if ctx.Err() != nil {
			break
		}
		r.started("session", u.key)
		it := r.session(ctx, src, u, &subagents)
		if it.Status == StatusError && ctx.Err() != nil {
			break // the copy it was in stopped with the run: nothing is left behind, the next run does it
		}
		r.add(it)
		r.finished(it)
	}
	if subagents > 0 {
		r.add(Item{Kind: "session", Key: "(subagent transcripts)", Status: StatusNoted, Note: fmt.Sprintf("%d files copied and uploaded with their sessions", subagents)})
	}
}

// session imports one unit: the subagent transcripts first, so that the session's upload takes them along.
func (r *run) session(ctx context.Context, src string, u sessionUnit, subagents *int) Item {
	if r.o.HoldSession != nil && u.top {
		if why := r.o.HoldSession(u.id); why != "" {
			return Item{Kind: "session", Key: u.key, Status: StatusSkipped, Note: why}
		}
	}
	for _, rel := range u.extras {
		*subagents++
		if status, note := r.copySession(ctx, filepath.Join(src, rel), filepath.Join(r.o.ProjectsDir, rel)); status == StatusError && ctx.Err() == nil {
			it := Item{Kind: "session", Key: filepath.ToSlash(rel), Status: status, Note: note}
			r.add(it)
			r.report(it)
		}
	}
	dst := filepath.Join(r.o.ProjectsDir, u.path)
	status, note := r.copySession(ctx, filepath.Join(src, u.path), dst)
	if !u.top {
		*subagents++
	}
	if !u.top || status == StatusError || r.o.DryRun || r.o.Queue == nil {
		return Item{Kind: "session", Key: u.key, Status: status, Note: note}
	}
	if status != StatusCopied && r.o.SessionDone != nil && r.o.SessionDone(u.id) {
		return Item{Kind: "session", Key: u.key, Status: status, Note: note}
	}
	if err := r.o.Queue.Queue(u.id, dst); err != nil {
		return Item{Kind: "session", Key: u.key, Status: StatusError, Note: joinNote(note, "queue: "+err.Error())}
	}
	if !r.o.UploadSessions {
		return Item{Kind: "session", Key: u.key, Status: StatusQueued, Note: note}
	}
	if err := r.o.Queue.Flush(ctx, u.id); err != nil {
		return Item{Kind: "session", Key: u.key, Status: StatusQueued, Note: joinNote(note, "upload deferred: "+err.Error())}
	}
	return Item{Kind: "session", Key: u.key, Status: StatusUploaded, Note: note}
}

// copyStatus says what importing src to dst would do: nothing (StatusExists, StatusSkipped) or copy (StatusPlanned).
func copyStatus(src, dst string) (Status, string, error) {
	si, err := os.Stat(src)
	if err != nil {
		return StatusError, "", err
	}
	if di, err := os.Stat(dst); err == nil {
		if di.Size() == si.Size() {
			return StatusExists, "already copied", nil
		}
		if di.Size() > si.Size() {
			return StatusSkipped, "local copy is newer (continued in harness)", nil
		}
	}
	return StatusPlanned, fmt.Sprintf("would copy %d bytes", si.Size()), nil
}

// copySession copies src to dst unless dst already has the same size.
func (r *run) copySession(ctx context.Context, src, dst string) (Status, string) {
	status, note, err := copyStatus(src, dst)
	switch {
	case err != nil:
		return StatusError, err.Error()
	case status != StatusPlanned || r.o.DryRun:
		return status, note
	}
	if err := copyFile(ctx, src, dst); err != nil {
		return StatusError, err.Error()
	}
	return StatusCopied, ""
}

// ctxReader stops a copy when its context ends: a transcript can take a while.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// copyFile copies through a temporary file, so a stopped copy leaves nothing behind. The copy keeps the source's time:
// session lists sort by it, and an imported session was not used just now.
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".import-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, ctxReader{ctx, in}); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chtimes(tmp, st.ModTime(), st.ModTime())
	return os.Rename(tmp, dst)
}
