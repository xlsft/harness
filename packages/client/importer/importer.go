// Package importer implements `harness-client import claude`: a one-time
// import of the user-level Claude Code configuration (~/.claude) into the
// harness server, and optionally of Claude Code session transcripts into
// ~/.harness/projects with upload to the server.
//
// Imported:
//
//	~/.claude/CLAUDE.md              → MEMORY (user, key "user")
//	~/.claude/settings.json          → SETTINGS (user, key "user"), minus permission rules and hooks
//	  permissions.allow/ask/deny     → PERMISSION_RULES (user, key "user")
//	  hooks                          → HOOK (user, key "user")
//	~/.claude/skills/<name>/         → SKILL (key name; SKILL.md + files)
//	~/.claude/agents/*.md            → AGENT (key: frontmatter name or file name)
//	~/.claude/commands/**/*.md       → COMMAND (key: relative path, "/" → ":")
//	~/.claude/output-styles/*.md     → OUTPUT_STYLE (key: frontmatter name or file name)
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
	o   Options
	rep *Report
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
	r := &run{o: o, rep: &Report{}}
	r.memory()
	r.settings()
	r.skills()
	r.markdownDir("agent", syncv1.DocumentKind_DOCUMENT_KIND_AGENT, filepath.Join(o.ClaudeHome, "agents"), false)
	r.markdownDir("command", syncv1.DocumentKind_DOCUMENT_KIND_COMMAND, filepath.Join(o.ClaudeHome, "commands"), true)
	r.markdownDir("output-style", syncv1.DocumentKind_DOCUMENT_KIND_OUTPUT_STYLE, filepath.Join(o.ClaudeHome, "output-styles"), false)
	r.mcp()
	if !o.DryRun {
		if err := r.upload(ctx); err != nil {
			return r.rep, err
		}
	}
	if o.Sessions {
		r.sessions(ctx)
	}
	return r.rep, nil
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
	note := ""
	if strings.Contains(string(data), "@") {
		note = "@imports are not resolved; imported files stay local"
	}
	r.doc(syncv1.DocumentKind_DOCUMENT_KIND_MEMORY, "memory", "user", string(data), nil, note)
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
		r.doc(syncv1.DocumentKind_DOCUMENT_KIND_HOOK, "hooks", "user", prettyJSON(raw), nil, "runs only after it is trusted on each device")
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
				notes = append(notes, "secret env not uploaded: "+strings.Join(dropped, ", ")+" (use --include-secrets)")
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
		end := min(start+batchSize, len(idx))
		chunk := idx[start:end]
		req := &syncv1.BatchPutDocumentsRequest{}
		for _, i := range chunk {
			req.Items = append(req.Items, r.rep.Items[i].req)
		}
		resp, err := r.o.Putter.BatchPutDocuments(ctx, req)
		if err != nil {
			for _, i := range chunk {
				r.rep.Items[i].Status = StatusError
				r.rep.Items[i].Note = joinNote(r.rep.Items[i].Note, err.Error())
			}
			return fmt.Errorf("upload: %w", err)
		}
		for j, i := range chunk {
			it := &r.rep.Items[i]
			if j >= len(resp.GetResults()) {
				it.Status, it.Note = StatusError, joinNote(it.Note, "no result from server")
				continue
			}
			switch res := resp.GetResults()[j].GetResult().(type) {
			case *syncv1.BatchPutResult_Document:
				it.Status = StatusUploaded
				it.Note = joinNote(it.Note, fmt.Sprintf("v%d", res.Document.GetVersion()))
			case *syncv1.BatchPutResult_Conflict:
				it.Status = StatusExists
				it.Note = joinNote(it.Note, "already on the server (use --force to overwrite)")
			case *syncv1.BatchPutResult_Error:
				it.Status = StatusError
				it.Note = joinNote(it.Note, res.Error.GetCode()+": "+res.Error.GetMessage())
			default:
				it.Status, it.Note = StatusError, joinNote(it.Note, "empty result")
			}
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

func (r *run) sessions(ctx context.Context) {
	src := filepath.Join(r.o.ClaudeHome, "projects")
	if r.o.ProjectsDir == "" {
		r.add(Item{Kind: "session", Key: src, Status: StatusError, Note: "no harness projects directory configured"})
		return
	}
	var files []string
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	var subagents int
	for _, p := range files {
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(r.o.ProjectsDir, rel)
		// Top-level transcripts are <slug>/<uuid>.jsonl; deeper files are
		// subagent transcripts that travel with their session.
		top := len(strings.Split(filepath.ToSlash(rel), "/")) == 2 && sessionFile.MatchString(filepath.Base(p))
		if !top {
			subagents++
		}
		key := filepath.ToSlash(rel)
		status, note := r.copySession(p, dst)
		if !top {
			if status == StatusError {
				r.add(Item{Kind: "session", Key: key, Status: status, Note: note})
			}
			continue
		}
		if status == StatusError || r.o.DryRun || r.o.Queue == nil {
			r.add(Item{Kind: "session", Key: key, Status: status, Note: note})
			continue
		}
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if err := r.o.Queue.Queue(id, dst); err != nil {
			r.add(Item{Kind: "session", Key: key, Status: StatusError, Note: joinNote(note, "queue: "+err.Error())})
			continue
		}
		if r.o.UploadSessions {
			if err := r.o.Queue.Flush(ctx, id); err != nil {
				r.add(Item{Kind: "session", Key: key, Status: StatusQueued, Note: joinNote(note, "upload deferred: "+err.Error())})
				continue
			}
			r.add(Item{Kind: "session", Key: key, Status: StatusUploaded, Note: note})
			continue
		}
		r.add(Item{Kind: "session", Key: key, Status: StatusQueued, Note: note})
	}
	if subagents > 0 {
		r.add(Item{Kind: "session", Key: "(subagent transcripts)", Status: StatusNoted, Note: fmt.Sprintf("%d files copied and uploaded with their sessions", subagents)})
	}
}

// copySession copies src to dst unless dst already has the same size.
func (r *run) copySession(src, dst string) (Status, string) {
	si, err := os.Stat(src)
	if err != nil {
		return StatusError, err.Error()
	}
	if di, err := os.Stat(dst); err == nil {
		if di.Size() == si.Size() {
			return StatusExists, "already copied"
		}
		if di.Size() > si.Size() {
			return StatusSkipped, "local copy is newer (continued in harness)"
		}
	}
	if r.o.DryRun {
		return StatusPlanned, fmt.Sprintf("would copy %d bytes", si.Size())
	}
	if err := copyFile(src, dst); err != nil {
		return StatusError, err.Error()
	}
	return StatusCopied, ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".import-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
