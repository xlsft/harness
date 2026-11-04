package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Show targets.
const (
	ShowFile = "file"
	ShowDiff = "diff"
	ShowURL  = "url"
	// ShowTerminal is not a target of the Show tool: the terminal tools bring up the tab they started.
	ShowTerminal = "terminal"
)

// ShowTarget is what the Show tool opens for the user.
type ShowTarget struct {
	// Kind is ShowFile, ShowDiff, ShowURL or ShowTerminal.
	Kind string
	// Path is absolute (file, diff; empty for the whole diff).
	Path string
	// LineStart and LineEnd are 1-based and inclusive; 0 when not given.
	LineStart, LineEnd int
	URL                string
	// TerminalID is the tab (ShowTerminal).
	TerminalID string
}

// ShowStatus is the outcome of Shower.Show.
type ShowStatus int

// Show outcomes.
const (
	// ShowOpened: a frontend watching the session opens the target now.
	ShowOpened ShowStatus = iota + 1
	// ShowPending: nobody watches the session; the target opens when the
	// user opens it.
	ShowPending
	// ShowUnavailable: the frontends cannot open panes (terminal UI).
	ShowUnavailable
)

// Shower is implemented by an Interactor whose frontends can open panes
// (AgentService).
type Shower interface {
	Show(toolUseID string, t ShowTarget) ShowStatus
}

// Show opens a file, the session diff or a web page in the user's UI. It
// only opens: nothing changes, so it needs no permission.
type Show struct{}

func (*Show) Name() string                  { return "Show" }
func (*Show) ReadOnly(json.RawMessage) bool { return true }

// maxShowLineCount is the largest file whose lines are counted to check the
// requested range.
const maxShowLineCount = 16 << 20

// ParseShow decodes and validates a Show input against env (paths must exist
// inside the working directories).
func ParseShow(env *Env, input json.RawMessage) (ShowTarget, error) {
	var in struct {
		Target  string `json:"target"`
		Path    string `json:"path"`
		Line    int    `json:"line"`
		EndLine int    `json:"end_line"`
		URL     string `json:"url"`
	}
	if err := decode(input, &in); err != nil {
		return ShowTarget{}, err
	}
	bad := func(format string, args ...any) (ShowTarget, error) {
		return ShowTarget{}, fmt.Errorf("InputValidationError: "+format, args...)
	}
	if in.Target != ShowFile && (in.Line != 0 || in.EndLine != 0) {
		return bad("line and end_line apply to target \"file\" only")
	}
	if in.Target != ShowURL && in.URL != "" {
		return bad("url applies to target \"url\" only")
	}
	t := ShowTarget{Kind: in.Target}
	switch in.Target {
	case ShowFile:
		if strings.TrimSpace(in.Path) == "" {
			return bad("path is required for target \"file\"")
		}
		t.Path = resolvePath(env.CurrentDir(), in.Path)
		if !within(t.Path, env.WorkingDirs()) {
			return bad("%s is outside the working directories", t.Path)
		}
		st, err := os.Stat(t.Path)
		if err != nil {
			return bad("file does not exist: %s", t.Path)
		}
		if st.IsDir() {
			return bad("%s is a directory; Show opens files", t.Path)
		}
		if in.Line < 0 || in.EndLine < 0 {
			return bad("line numbers start at 1")
		}
		if in.EndLine != 0 && in.Line == 0 {
			return bad("end_line needs line")
		}
		if in.EndLine != 0 && in.EndLine < in.Line {
			return bad("end_line (%d) is before line (%d)", in.EndLine, in.Line)
		}
		if in.Line > 0 && st.Size() <= maxShowLineCount {
			if data, err := os.ReadFile(t.Path); err == nil {
				n := bytes.Count(data, []byte{'\n'})
				if len(data) > 0 && data[len(data)-1] != '\n' {
					n++
				}
				if in.Line > n {
					return bad("line %d is past the end of the file (%d lines)", in.Line, n)
				}
				in.EndLine = min(in.EndLine, n)
			}
		}
		t.LineStart, t.LineEnd = in.Line, in.EndLine
	case ShowDiff:
		if strings.TrimSpace(in.Path) != "" {
			t.Path = resolvePath(env.CurrentDir(), in.Path)
			if !within(t.Path, env.WorkingDirs()) {
				return bad("%s is outside the working directories", t.Path)
			}
		}
	case ShowURL:
		u, err := url.Parse(strings.TrimSpace(in.URL))
		if err != nil || in.URL == "" {
			return bad("url must be an absolute http or https URL")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return bad("url scheme must be http or https, got %q", u.Scheme)
		}
		if u.Host == "" {
			return bad("url must include a host")
		}
		t.URL = u.String()
	default:
		return bad("target must be \"file\", \"diff\" or \"url\"")
	}
	return t, nil
}

// ShowLabel renders the target for people: "src/x.go:10-20", "the diff of
// src/x.go", a URL. Paths are relative to cwd when inside it.
func ShowLabel(t ShowTarget, cwd string) string {
	rel := t.Path
	if r, err := filepath.Rel(cwd, t.Path); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		rel = filepath.ToSlash(r)
	}
	switch t.Kind {
	case ShowFile:
		switch {
		case t.LineEnd > t.LineStart:
			return fmt.Sprintf("%s:%d-%d", rel, t.LineStart, t.LineEnd)
		case t.LineStart > 0:
			return fmt.Sprintf("%s:%d", rel, t.LineStart)
		}
		return rel
	case ShowDiff:
		if t.Path == "" {
			return "the session diff"
		}
		return "the diff of " + rel
	}
	return t.URL
}

func showPane(kind string) string {
	switch kind {
	case ShowFile:
		return "Files"
	case ShowDiff:
		return "Diff"
	}
	return "Browser"
}

func (*Show) Run(ctx context.Context, env *Env, call Call) Result {
	t, err := ParseShow(env, call.Input)
	if err != nil {
		return ErrorResult("%v", err)
	}
	label := ShowLabel(t, env.CurrentDir())
	structured := map[string]any{"target": t.Kind, "label": label}
	if t.Path != "" {
		structured["path"] = t.Path
	}
	if t.LineStart > 0 {
		structured["line"] = t.LineStart
	}
	if t.LineEnd > 0 {
		structured["end_line"] = t.LineEnd
	}
	if t.URL != "" {
		structured["url"] = t.URL
	}
	var sh Shower
	if h := env.Host(); h != nil {
		if ia := h.Interactor(); ia != nil {
			sh, _ = ia.(Shower)
		}
	}
	pane := showPane(t.Kind)
	status := ShowUnavailable
	if sh != nil {
		status = sh.Show(call.ID, t)
	}
	structured["shown"] = status != ShowUnavailable
	var r Result
	switch status {
	case ShowOpened:
		r = TextResult(fmt.Sprintf("Opened %s in the %s pane.", label, pane), structured)
	case ShowPending:
		structured["pending"] = true
		r = TextResult(fmt.Sprintf("Requested %s in the %s pane. Nobody is viewing this session right now; it opens when the user does.", label, pane), structured)
	default:
		r = TextResult(fmt.Sprintf("Nothing was shown: no UI that can open it is attached to this session. If the user needs to look at %s, name it in your reply.", label), structured)
		r.Summary = "Not shown (no UI)"
		return r
	}
	r.Summary = "Opened " + label
	return r
}
