package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func describe(in Instruction) string {
	var d string
	switch in.Origin {
	case OriginManaged:
		d = "organization-managed instructions"
	case OriginUser:
		d = "user's private global instructions for all projects"
	case OriginLocal:
		d = "user's private project instructions, not checked in"
	case OriginAgents:
		d = "project instructions (AGENTS.md), checked into the codebase"
	case OriginRule:
		d = "project rule"
		if len(in.Globs) > 0 {
			d += " for files matching " + strings.Join(in.Globs, ", ")
		}
	case OriginAutoMemory:
		d = "auto memory index for this project; topic files live next to it"
	default:
		d = "project instructions, checked into the codebase"
	}
	if in.ImportedFrom != "" {
		d += "; imported by " + in.ImportedFrom
	}
	return d
}

// Render formats instructions as plain data for the prompt bundle's
// project_instructions reminder ({{content}}).
func Render(instrs []Instruction) string {
	parts := make([]string, 0, len(instrs))
	for _, in := range instrs {
		parts = append(parts, "Contents of "+in.Path+" ("+describe(in)+"):\n\n"+in.Content)
	}
	return strings.Join(parts, "\n\n")
}

// ProjectContext renders instructions followed by an optional git snapshot,
// the shape the agent passes as its project context.
func ProjectContext(instrs []Instruction, gitStatus string) string {
	s := Render(instrs)
	if gitStatus != "" {
		if s != "" {
			s += "\n\n"
		}
		s += "gitStatus:\n" + gitStatus
	}
	return s
}

// FileApprovals persists import approvals in a JSON file shared by all
// projects: {"<project>": {"<path>": true|false}}.
type FileApprovals struct {
	Path    string
	Project string

	mu sync.Mutex
}

func (f *FileApprovals) load() map[string]map[string]bool {
	m := map[string]map[string]bool{}
	data, err := os.ReadFile(f.Path)
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// Decision implements Approvals.
func (f *FileApprovals) Decision(path string) (approved, decided bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.load()[f.Project][path]
	return v, ok
}

// Record implements Approvals.
func (f *FileApprovals) Record(path string, approved bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Path == "" {
		return errors.New("memory: FileApprovals.Path is empty")
	}
	m := f.load()
	if m[f.Project] == nil {
		m[f.Project] = map[string]bool{}
	}
	m[f.Project][path] = approved
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// AutoMemoryDir returns the local auto memory directory for a project key
// under the harness home (e.g. ~/.harness/memory/<key>/).
func AutoMemoryDir(harnessHome, projectKey string) string {
	var b strings.Builder
	for _, r := range projectKey {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return filepath.Join(harnessHome, "memory", b.String())
}
