package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/tools"
)

const (
	// maxReminderErrors and maxReminderBytes cap one reminder.
	maxReminderErrors = 20
	maxReminderBytes  = 4000
	// maxErrorChars caps the message of one error.
	maxErrorChars = 300
)

var diagnosticBlockRe = regexp.MustCompile(`(?i)<(/?)(system-reminder|lsp-diagnostics)`)

// FileEdited implements tools.EditWatcher: after a file tool wrote path, the server of that file gets the new text, and
// what it publishes goes to the agent's next request. The edit waits for that only up to EditWait; what comes later,
// within LateWait, rides on a later request.
func (m *Manager) FileEdited(ctx context.Context, env *tools.Env, path string) {
	if m == nil {
		return
	}
	cfg := m.config()
	if !cfg.Enabled || !cfg.Diagnostics {
		return
	}
	real := realPath(path)
	spec, language := specFor(specTable(cfg.Servers), real)
	if spec == nil || spec.disabled {
		return
	}
	at := placeOf(env)
	srv, err := m.acquire(spec, real, at, cfg.StartForDiagnostics)
	if err != nil {
		m.log.Debug("no language server after an edit", "path", path, "err", err)
		return
	}
	if srv == nil {
		return
	}
	done := make(chan struct{})
	agentID := env.AgentID
	go func() {
		defer close(done)
		bg, cancel := context.WithTimeout(context.Background(), m.opts.RequestTimeout)
		defer cancel()
		if srv.waitReady(bg) != nil {
			return
		}
		uri := pathURI(real)
		mark := srv.collect(agentID, at.cwd, at.dirs, uri, time.Now().Add(m.opts.LateWait))
		if srv.refreshOpenDocuments(bg) != nil {
			return
		}
		if _, err := srv.syncDocument(bg, real, language, true); err != nil {
			return
		}
		srv.waitPublished(bg, uri, mark, publishSettle)
	}()
	wait := time.NewTimer(m.opts.EditWait)
	defer wait.Stop()
	select {
	case <-done:
	case <-wait.C:
	case <-ctx.Done():
	}
}

// PendingDiagnostics implements tools.EditWatcher: the errors the agent should hear about now, as text for the
// lsp_diagnostics reminder ("" when there are none). Taking them is what marks them as told.
func (m *Manager) PendingDiagnostics(agentID string) string {
	if m == nil {
		return ""
	}
	cfg := m.config()
	if !cfg.Enabled || !cfg.Diagnostics {
		return ""
	}
	budget, more := maxReminderErrors, 0
	var lines []string
	cache := fileLines{}
	keep := func(c *collector, path string) bool {
		shown, inside := sessionPath(path, c.dirs)
		return inside && !m.readDenied(shown)
	}
	size := 0
	for _, s := range m.servers() {
		errs, over, cwd, dirs := s.takeErrors(agentID, budget, keep)
		budget -= len(errs)
		more += over
		for _, e := range errs {
			shown, _ := sessionPath(e.path, dirs)
			line := diagnosticLine(displayPath(shown, cwd), e, cache)
			if size+len(line) > maxReminderBytes {
				more++
				continue
			}
			size += len(line) + 1
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("... and %d more", more))
	}
	return strings.Join(lines, "\n")
}

func diagnosticLine(path string, e pendingError, cache fileLines) string {
	start := e.item.Range.Start
	char := charOffset(cache.line(e.path, start.Line), start.Character)
	msg := strings.Join(strings.Fields(e.item.Message), " ")
	if r := []rune(msg); len(r) > maxErrorChars {
		msg = string(r[:maxErrorChars]) + "..."
	}
	// A message can quote the source: it must not be able to end the block it travels in.
	msg = diagnosticBlockRe.ReplaceAllString(msg, `<\$1$2`)
	tag := e.item.Source
	if c := e.item.code(); c != "" {
		tag = strings.TrimSpace(tag + " " + c)
	}
	if tag != "" {
		tag = " [" + tag + "]"
	}
	return fmt.Sprintf("%s:%d:%d: %s%s", path, start.Line+1, char+1, msg, tag)
}

// fileLines reads files once per call, to turn UTF-16 columns into characters and to show source lines.
type fileLines map[string][]string

func (f fileLines) line(path string, n int) string {
	lines, ok := f[path]
	if !ok {
		if st, err := os.Stat(path); err == nil && st.Size() <= maxDocumentBytes {
			if data, err := os.ReadFile(filepath.Clean(path)); err == nil {
				lines = strings.Split(string(data), "\n")
			}
		}
		f[path] = lines
	}
	if n < 0 || n >= len(lines) {
		return ""
	}
	return strings.TrimRight(lines[n], "\r")
}
