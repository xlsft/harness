package ide

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	idev1 "github.com/xlsft/harness/packages/client/gen/harness/ide/v1"
	"github.com/xlsft/harness/packages/client/tools"
)

const (
	// maxDiagnostics is how many problems one call returns; maxDiagnosticChars how long a message may be.
	maxDiagnostics     = 200
	maxDiagnosticChars = 400
)

// Text a diagnostic must not smuggle in: its message comes from whatever linter the workspace configured.
var diagnosticTagRe = regexp.MustCompile(`(?i)<(/?)(system-reminder|editor-selection)`)

// Tool returns the EditorDiagnostics tool. cwd says where the session is now.
func (m *Manager) Tool(cwd func() string) tools.Tool { return &diagnosticsTool{m: m, cwd: cwd} }

type diagnosticsTool struct {
	m   *Manager
	cwd func() string
}

func (*diagnosticsTool) Name() string                  { return "EditorDiagnostics" }
func (*diagnosticsTool) ReadOnly(json.RawMessage) bool { return true }

// HiddenFromModel implements tools.ModelHider: without an editor there is nothing to ask.
func (t *diagnosticsTool) HiddenFromModel() bool {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	return t.m.For(ctx, t.cwd()) == nil
}

func (t *diagnosticsTool) Run(ctx context.Context, env *tools.Env, call tools.Call) tools.Result {
	var in struct {
		Path     string `json:"path"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return tools.ErrorResult("InputValidationError: %v", err)
	}
	worst := idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_INFORMATION
	switch in.Severity {
	case "", "information":
	case "error":
		worst = idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR
	case "warning":
		worst = idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARNING
	case "hint":
		worst = idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_HINT
	default:
		return tools.ErrorResult("InputValidationError: severity must be one of error, warning, information, hint")
	}
	ed := t.m.For(ctx, env.CurrentDir())
	if ed == nil {
		return tools.ErrorResult("No editor is attached to this session.")
	}
	var paths []string
	if in.Path != "" {
		p := in.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(env.CurrentDir(), p)
		}
		paths = []string{filepath.Clean(p)}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	found, err := ed.Diagnostics(ctx, paths)
	if err != nil {
		return tools.ErrorResult("%s did not answer: %v", ed.Name(), err)
	}
	return formatDiagnostics(found, env, worst, ed.Name())
}

// inScope keeps the problems to the session's folders: the tool needs no permission, so it shows no more than a read
// inside them would.
func inScope(env *tools.Env, path string) bool {
	if env.ReadDenied != nil && env.ReadDenied(path) {
		return false
	}
	real := resolved(path)
	for _, d := range env.WorkingDirs() {
		if _, ok := within(resolved(d), real); ok {
			return true
		}
	}
	return false
}

func formatDiagnostics(found []*idev1.Diagnostic, env *tools.Env, worst idev1.DiagnosticSeverity, editor string) tools.Result {
	names := map[idev1.DiagnosticSeverity]string{
		idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR:       "error",
		idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARNING:     "warning",
		idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_INFORMATION: "information",
		idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_HINT:        "hint",
	}
	var b strings.Builder
	shown, errors, warnings, more, outside := 0, 0, 0, 0, 0
	for _, d := range found {
		if !inScope(env, d.GetPath()) {
			outside++
			continue
		}
		if s := d.GetSeverity(); s == idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_UNSPECIFIED || s > worst {
			continue
		}
		switch d.GetSeverity() {
		case idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR:
			errors++
		case idev1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARNING:
			warnings++
		}
		if shown == maxDiagnostics {
			more++
			continue
		}
		shown++
		fmt.Fprintf(&b, "%s:%d:%d: %s", d.GetPath(), d.GetLine(), d.GetColumn(), names[d.GetSeverity()])
		if src := strings.TrimSpace(d.GetSource() + " " + d.GetCode()); src != "" {
			fmt.Fprintf(&b, " [%s]", src)
		}
		fmt.Fprintf(&b, ": %s\n", cleanMessage(d.GetMessage()))
	}
	if more > 0 {
		fmt.Fprintf(&b, "... and %d more; ask for one path or for errors only.\n", more)
	}
	if outside > 0 {
		fmt.Fprintf(&b, "(%d outside this session's folders left out.)\n", outside)
	}
	text := b.String()
	if shown == 0 && more == 0 {
		text = "No problems reported by " + editor + ".\n" + text
	}
	r := tools.TextResult(strings.TrimRight(text, "\n"), map[string]any{"count": shown + more, "errors": errors, "warnings": warnings, "editor": editor})
	r.Summary = fmt.Sprintf("%d errors, %d warnings", errors, warnings)
	return r
}

func cleanMessage(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = diagnosticTagRe.ReplaceAllString(s, "&lt;$1$2")
	if r := []rune(s); len(r) > maxDiagnosticChars {
		s = string(r[:maxDiagnosticChars]) + "..."
	}
	return s
}
