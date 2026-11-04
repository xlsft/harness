package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// Scanning: a preview of what Run would do, for a frontend to choose from. The config goes through Run's dry-run path.
// Sessions are found by one walk over the projects directory, with the size and time from it and the rest from the
// head of each transcript, so a directory of gigabytes takes as long as its file count.

const (
	// headMaxBytes bounds what is read of one transcript; headLineBytes is the longest line looked at (longer ones are
	// pasted images and tool output, with nothing to tell).
	headMaxBytes  = 256 << 10
	headLineBytes = 64 << 10
	// existsWorkers and existsTimeout bound asking the server which documents exist.
	existsWorkers = 8
	existsTimeout = 5 * time.Second
)

// ScanOptions configure Scan.
type ScanOptions struct {
	ClaudeHome, ClaudeJSON, ProjectsDir string
	// Exists says whether the server has a document; nil without a server: nothing is known to exist.
	Exists func(ctx context.Context, ref *syncv1.DocumentRef) (bool, error)
	// HoldSession and SessionDone are as in Options.
	HoldSession func(id string) string
	SessionDone func(id string) bool
	// SecretsHint is as in Options.
	SecretsHint string
}

// ScanResult is what Scan found.
type ScanResult struct {
	// Found says ClaudeHome exists.
	Found bool
	// Items are the config items: StatusPlanned (new), StatusExists, StatusSkipped, StatusNoted (manual), StatusError.
	Items    []Item
	Projects []Project
	// ServerChecked says every planned item was looked up on the server.
	ServerChecked bool
}

// Project is a folder of ClaudeHome/projects, with its sessions newest first.
type Project struct {
	Folder string
	// Path is the directory the sessions ran in (the newest that says), else Folder.
	Path     string
	Sessions []Session
	// Imported counts the sessions with StatusExists or StatusSkipped.
	Imported int
	Bytes    int64
	Newest   time.Time
}

// Session is one transcript found by a scan. Status is StatusPlanned (an import brings it), StatusExists or
// StatusSkipped.
type Session struct {
	ID, Title string
	Bytes     int64
	Modified  time.Time
	Status    Status
	Note      string
}

// Scan previews an import. It writes nothing.
func Scan(ctx context.Context, o ScanOptions) (*ScanResult, error) {
	if o.ClaudeHome == "" {
		return nil, errors.New("importer: ClaudeHome is required")
	}
	st, err := os.Stat(o.ClaudeHome)
	res := &ScanResult{Found: err == nil && st.IsDir(), ServerChecked: o.Exists != nil}
	if !res.Found {
		return res, nil
	}
	rep, err := Run(ctx, Options{ClaudeHome: o.ClaudeHome, ClaudeJSON: o.ClaudeJSON, ProjectsDir: o.ProjectsDir, DryRun: true, SecretsHint: o.SecretsHint})
	if err != nil {
		return nil, err
	}
	if o.Exists != nil {
		res.ServerChecked = markExisting(ctx, rep.Items, o.Exists)
	}
	res.Items = rep.Items
	res.Projects = ScanSessions(ctx, o)
	return res, ctx.Err()
}

// markExisting asks the server about the planned documents; false when it did not answer them all.
func markExisting(ctx context.Context, items []Item, exists func(context.Context, *syncv1.DocumentRef) (bool, error)) bool {
	ctx, cancel := context.WithTimeout(ctx, existsTimeout)
	defer cancel()
	sem := make(chan struct{}, existsWorkers)
	var wg sync.WaitGroup
	var failed atomic.Bool
	for i := range items {
		it := &items[i]
		if it.req == nil || it.Status != StatusPlanned {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ok, err := exists(ctx, it.req.GetRef())
			switch {
			case err != nil:
				failed.Store(true)
			case ok:
				it.Status = StatusExists
			}
		}()
	}
	wg.Wait()
	return !failed.Load()
}

// ScanSessions groups the transcripts under ClaudeHome/projects by folder.
func ScanSessions(ctx context.Context, o ScanOptions) []Project {
	src := filepath.Join(o.ClaudeHome, "projects")
	type found struct {
		path  string
		bytes int64
		mod   time.Time
	}
	sessions := map[[2]string]*found{}
	extras := map[[2]string]int64{}
	folderBytes := map[string]int64{}
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		switch {
		case len(parts) < 2:
		case len(parts) == 2 && sessionFile.MatchString(parts[1]):
			id := strings.TrimSuffix(parts[1], ".jsonl")
			sessions[[2]string{parts[0], id}] = &found{p, info.Size(), info.ModTime()}
			folderBytes[parts[0]] += info.Size()
		case len(parts) > 2:
			extras[[2]string{parts[0], parts[1]}] += info.Size()
			folderBytes[parts[0]] += info.Size()
		}
		return nil
	})

	projects := map[string]*Project{}
	pathTime := map[string]time.Time{}
	for key, f := range sessions {
		if ctx.Err() != nil {
			break
		}
		folder, id := key[0], key[1]
		pr := projects[folder]
		if pr == nil {
			pr = &Project{Folder: folder, Path: folder, Bytes: folderBytes[folder]}
			projects[folder] = pr
		}
		cwd, title := transcriptHead(f.path)
		s := Session{ID: id, Title: title, Bytes: f.bytes + extras[key], Modified: f.mod}
		s.Status, s.Note = o.sessionStatus(f.path, filepath.Join(o.ProjectsDir, folder, id+".jsonl"), id)
		if s.Status == StatusExists || s.Status == StatusSkipped {
			pr.Imported++
		}
		pr.Sessions = append(pr.Sessions, s)
		if f.mod.After(pr.Newest) {
			pr.Newest = f.mod
		}
		if t, ok := pathTime[folder]; cwd != "" && (!ok || f.mod.After(t)) {
			pr.Path, pathTime[folder] = cwd, f.mod
		}
	}
	out := make([]Project, 0, len(projects))
	for _, pr := range projects {
		sort.Slice(pr.Sessions, func(i, j int) bool {
			a, b := pr.Sessions[i], pr.Sessions[j]
			if !a.Modified.Equal(b.Modified) {
				return a.Modified.After(b.Modified)
			}
			return a.ID < b.ID
		})
		out = append(out, *pr)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Newest.Equal(out[j].Newest) {
			return out[i].Newest.After(out[j].Newest)
		}
		return out[i].Folder < out[j].Folder
	})
	return out
}

// sessionStatus is what importing a session would do. A copy that is current but not all on the server still counts
// as new: importing it again finishes the upload.
func (o ScanOptions) sessionStatus(src, dst, id string) (Status, string) {
	if o.HoldSession != nil {
		if why := o.HoldSession(id); why != "" {
			return StatusSkipped, why
		}
	}
	status, note, err := copyStatus(src, dst)
	if err != nil {
		return StatusSkipped, err.Error()
	}
	if status == StatusPlanned {
		if _, err := os.Stat(dst); err == nil {
			return StatusPlanned, "has newer messages than its import"
		}
		return StatusPlanned, ""
	}
	if o.SessionDone == nil || !o.SessionDone(id) {
		return StatusPlanned, "copied here, its upload did not finish"
	}
	if status == StatusExists {
		return StatusExists, "already imported"
	}
	return status, note
}

// transcriptHead reads the start of a transcript for its directory and title, as far as it takes to know both. A custom
// or AI title further down the file goes unseen: the session then reads as its first prompt.
func transcriptHead(path string) (cwd, title string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	br := bufio.NewReaderSize(io.LimitReader(f, headMaxBytes), headLineBytes)
	var named, prompt string
	for cwd == "" || (named == "" && prompt == "") {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
		} else if len(line) > 0 {
			readHeadLine(line, &cwd, &named, &prompt)
		}
		if err != nil {
			break
		}
	}
	if named != "" {
		return cwd, named
	}
	return cwd, shortTitle(prompt)
}

func readHeadLine(line []byte, cwd, named, prompt *string) {
	var rec struct {
		Type        string `json:"type"`
		Cwd         string `json:"cwd"`
		CustomTitle string `json:"customTitle"`
		AITitle     string `json:"aiTitle"`
		Title       string `json:"title"`
		Summary     string `json:"summary"`
		IsMeta      bool   `json:"isMeta"`
		Message     *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if *cwd == "" {
		*cwd = rec.Cwd
	}
	switch rec.Type {
	case "custom-title":
		*named = firstNonEmpty(rec.CustomTitle, rec.Title)
	case "ai-title":
		if *named == "" {
			*named = firstNonEmpty(rec.AITitle, rec.Title)
		}
	case "summary":
		if *named == "" {
			*named = strings.TrimSpace(rec.Summary)
		}
	case "user":
		if *prompt == "" && !rec.IsMeta && rec.Message != nil {
			*prompt = promptText(rec.Message.Content)
		}
	}
}

// promptText is what the user typed: a plain string or the first text block. Command wrappers ("<command-name>...") and
// other markup are not a prompt.
func promptText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &blocks) != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "text" {
				s = b.Text
				break
			}
		}
	}
	if s = strings.TrimSpace(s); strings.HasPrefix(s, "<") {
		return ""
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func shortTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return s
}
