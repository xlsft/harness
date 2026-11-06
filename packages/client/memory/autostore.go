package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Auto memory is what the agent keeps for itself per project: topic files, one fact or preference each, and MEMORY.md,
// the index a new session loads. The index is built from the files' frontmatter and never edited by hand, so it cannot
// drift from them.
//
// A project's memory lives in a directory on this machine, the working copy, so it works offline. With a server it is
// also one synced document (scope project, kind memory, key "user": content is the index, files are the topic files).
// Conflicts are last-write-wins per file. What this machine changed and has not sent yet is listed in a pending file,
// and goes up on the next Sync.

const (
	// AutoMemoryIndex is the index file's name.
	AutoMemoryIndex = "MEMORY.md"
	// AutoMemoryMaxFileBytes caps one topic file.
	AutoMemoryMaxFileBytes = 64 << 10
	// AutoMemoryMaxFiles caps the files of a project.
	AutoMemoryMaxFiles = 200

	autoPendingFile = ".pending.json"
	autoProjectFile = ".project"
	autoSyncTries   = 3
)

var autoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

// AutoMemoryTypes are the kinds of memory a file can hold.
var AutoMemoryTypes = []string{"user", "feedback", "project", "reference"}

// AutoFile is one topic file.
type AutoFile struct {
	// Name is the file name, with .md.
	Name        string
	Title       string
	Description string
	Type        string
	// Content is the whole file, frontmatter included.
	Content string
}

// AutoDocs is the server side of auto memory (syncclient.AutoMemoryDocs).
type AutoDocs interface {
	// Load returns a project's document. found is false when there is none.
	Load(ctx context.Context, projectKey string) (index string, files map[string]string, version int64, found bool, err error)
	// Save writes it if base is still its version (0: it must not exist). conflict reports that it was not.
	Save(ctx context.Context, projectKey, index string, files map[string]string, base int64) (version int64, conflict bool, err error)
	// Projects lists the project keys that have a document.
	Projects(ctx context.Context) ([]string, error)
}

// AutoStore reads and writes one project's auto memory. Safe for concurrent use within a process.
type AutoStore struct {
	Dir        string
	ProjectKey string
	// Docs is nil without a server.
	Docs AutoDocs
}

// One lock per directory: the sessions of a project, and the settings screen, share its files.
var autoLocks sync.Map

func (s *AutoStore) lock() func() {
	v, _ := autoLocks.LoadOrStore(s.Dir, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// AutoFileName validates a memory's name and returns its file name.
func AutoFileName(name string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(name), ".md")
	if strings.EqualFold(base, "memory") || !autoNameRe.MatchString(base) {
		return "", fmt.Errorf("%q is not a memory name: use lowercase letters, digits, _ and -, and not MEMORY", name)
	}
	return base + ".md", nil
}

// BuildAutoFile writes a topic file's text from its parts.
func BuildAutoFile(title, description, kind, body string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\ntype: %s\n---\n\n%s\n", oneLine(title), oneLine(description), oneLine(kind), strings.TrimSpace(body))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ParseAutoFile reads a topic file's frontmatter; a file without one is described by its first line.
func ParseAutoFile(name, content string) AutoFile {
	f := AutoFile{Name: name, Content: content, Title: strings.TrimSuffix(name, ".md")}
	body := content
	if rest, ok := strings.CutPrefix(content, "---\n"); ok {
		if head, tail, ok := strings.Cut(rest, "\n---"); ok {
			body = strings.TrimPrefix(tail, "\n")
			for _, line := range strings.Split(head, "\n") {
				k, v, _ := strings.Cut(line, ":")
				v = strings.TrimSpace(v)
				switch strings.TrimSpace(k) {
				case "name":
					if v != "" {
						f.Title = v
					}
				case "description":
					f.Description = v
				case "type":
					f.Type = v
				}
			}
		}
	}
	if f.Description == "" {
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(strings.TrimLeft(line, "# ")); line != "" {
				f.Description = truncateRunes(line, 150)
				break
			}
		}
	}
	return f
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// BuildAutoIndex is MEMORY.md for these files: one line each, by name.
func BuildAutoIndex(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		f := ParseAutoFile(n, files[n])
		line := fmt.Sprintf("- [%s](%s)", oneLine(f.Title), n)
		if f.Description != "" {
			line += " - " + oneLine(f.Description)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

type autoPending struct {
	// Set and Deleted name the files changed here and not sent yet.
	Set     []string `json:"set,omitempty"`
	Deleted []string `json:"deleted,omitempty"`
	// Version is the document's version this copy is at.
	Version int64 `json:"version,omitempty"`
}

func (p *autoPending) dirty() bool { return len(p.Set)+len(p.Deleted) > 0 }

func without(list []string, name string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != name {
			out = append(out, x)
		}
	}
	return out
}

func (p *autoPending) markSet(name string) {
	p.Deleted = without(p.Deleted, name)
	p.Set = append(without(p.Set, name), name)
}

func (p *autoPending) markDeleted(name string) {
	p.Set = without(p.Set, name)
	p.Deleted = append(without(p.Deleted, name), name)
}

func (s *AutoStore) pendingPath() string { return filepath.Join(s.Dir, autoPendingFile) }

func (s *AutoStore) readPending() *autoPending {
	p := &autoPending{}
	if data, err := os.ReadFile(s.pendingPath()); err == nil {
		_ = json.Unmarshal(data, p)
	}
	return p
}

func (s *AutoStore) writePending(p *autoPending) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.writeFile(autoPendingFile, data)
}

func (s *AutoStore) writeFile(name string, data []byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, name))
}

// readTopics returns the topic files on disk by name.
func (s *AutoStore) readTopics() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == AutoMemoryIndex || !strings.HasSuffix(n, ".md") {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(s.Dir, n)); err == nil {
			out[n] = string(data)
		}
	}
	return out
}

func (s *AutoStore) writeIndex() error {
	return s.writeFile(AutoMemoryIndex, []byte(BuildAutoIndex(s.readTopics())))
}

// Index returns MEMORY.md as it is on this machine.
func (s *AutoStore) Index() string {
	data, err := os.ReadFile(filepath.Join(s.Dir, AutoMemoryIndex))
	if err != nil {
		return ""
	}
	return string(data)
}

// List returns the project's files, by name.
func (s *AutoStore) List() []AutoFile {
	defer s.lock()()
	topics := s.readTopics()
	names := make([]string, 0, len(topics))
	for n := range topics {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]AutoFile, 0, len(names))
	for _, n := range names {
		out = append(out, ParseAutoFile(n, topics[n]))
	}
	return out
}

// Read returns one file's text.
func (s *AutoStore) Read(name string) (string, error) {
	file, err := AutoFileName(name)
	if err != nil {
		return "", err
	}
	defer s.lock()()
	data, err := os.ReadFile(filepath.Join(s.Dir, file))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("no memory named %q", strings.TrimSuffix(file, ".md"))
	}
	return string(data), err
}

// Write creates or replaces one file and rebuilds the index. Sending it to the server is best effort: what did not make
// it stays pending. It returns the file name.
func (s *AutoStore) Write(ctx context.Context, name, content string) (string, error) {
	file, err := AutoFileName(name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New("a memory cannot be empty")
	}
	if len(content) > AutoMemoryMaxFileBytes {
		return "", fmt.Errorf("a memory has at most %d KB; split it", AutoMemoryMaxFileBytes>>10)
	}
	defer s.lock()()
	topics := s.readTopics()
	if _, exists := topics[file]; !exists && len(topics) >= AutoMemoryMaxFiles {
		return "", fmt.Errorf("this project has %d memories, the limit; delete what is out of date first", len(topics))
	}
	if err := s.writeFile(file, []byte(content)); err != nil {
		return "", err
	}
	p := s.readPending()
	p.markSet(file)
	if err := s.commit(ctx, p); err != nil {
		return "", err
	}
	return file, nil
}

// Delete removes one file and rebuilds the index.
func (s *AutoStore) Delete(ctx context.Context, name string) error {
	file, err := AutoFileName(name)
	if err != nil {
		return err
	}
	defer s.lock()()
	if err := os.Remove(filepath.Join(s.Dir, file)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no memory named %q", strings.TrimSuffix(file, ".md"))
		}
		return err
	}
	p := s.readPending()
	p.markDeleted(file)
	return s.commit(ctx, p)
}

func (s *AutoStore) commit(ctx context.Context, p *autoPending) error {
	if err := s.writeIndex(); err != nil {
		return err
	}
	if s.ProjectKey != "" {
		_ = s.writeFile(autoProjectFile, []byte(s.ProjectKey))
	}
	if err := s.writePending(p); err != nil {
		return err
	}
	// The change is safe on disk; a server that cannot be reached gets it later.
	_ = s.syncLocked(ctx)
	return nil
}

// Sync sends what is pending and brings this machine's copy up to the server's.
func (s *AutoStore) Sync(ctx context.Context) error {
	defer s.lock()()
	return s.syncLocked(ctx)
}

func (s *AutoStore) syncLocked(ctx context.Context) error {
	if s.Docs == nil || s.ProjectKey == "" {
		return nil
	}
	p := s.readPending()
	for range autoSyncTries {
		_, files, version, found, err := s.Docs.Load(ctx, s.ProjectKey)
		if err != nil {
			return err
		}
		if !found && !p.dirty() {
			// Files from before there was a server go up; with none, there is nothing to do.
			for n := range s.readTopics() {
				p.markSet(n)
			}
			if !p.dirty() {
				return nil
			}
		}
		if !p.dirty() {
			if version == p.Version {
				return nil
			}
			return s.mirror(files, version)
		}
		if files == nil {
			files = map[string]string{}
		}
		local := s.readTopics()
		for _, n := range p.Set {
			if text, ok := local[n]; ok {
				files[n] = text
			}
		}
		for _, n := range p.Deleted {
			delete(files, n)
		}
		base := int64(0)
		if found {
			base = version
		}
		saved, conflict, err := s.Docs.Save(ctx, s.ProjectKey, BuildAutoIndex(files), files, base)
		if err != nil {
			return err
		}
		if conflict {
			continue
		}
		return s.mirror(files, saved)
	}
	return errors.New("the memory document kept changing")
}

// mirror makes this machine's copy what the document says, with nothing pending.
func (s *AutoStore) mirror(files map[string]string, version int64) error {
	for n := range s.readTopics() {
		if _, ok := files[n]; !ok {
			_ = os.Remove(filepath.Join(s.Dir, n))
		}
	}
	for n, text := range files {
		if _, err := AutoFileName(n); err != nil {
			continue // Not something this client wrote; it stays in the document but not on disk.
		}
		if err := s.writeFile(n, []byte(text)); err != nil {
			return err
		}
	}
	if err := s.writeIndex(); err != nil {
		return err
	}
	if s.ProjectKey != "" {
		_ = s.writeFile(autoProjectFile, []byte(s.ProjectKey))
	}
	return s.writePending(&autoPending{Version: version})
}

// HasPendingAutoMemory reports whether dir holds changes the server has not seen: then its index is the newest there is.
func HasPendingAutoMemory(dir string) bool {
	s := AutoStore{Dir: dir}
	return s.readPending().dirty()
}

// LocalAutoMemoryProjects lists the project keys with a directory under the harness home.
func LocalAutoMemoryProjects(harnessHome string) []string {
	entries, err := os.ReadDir(filepath.Join(harnessHome, "memory"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(harnessHome, "memory", e.Name(), autoProjectFile)); err == nil && len(data) > 0 {
			out = append(out, string(data))
		}
	}
	return out
}
