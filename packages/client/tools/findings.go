package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// ReportFindings limits.
const (
	maxFindings        = 100
	maxFindingSummary  = 1000
	maxFindingScenario = 4000
)

// Severities, most severe first. The client derives them from the category and verdict, so the order of a list means
// the same on every device.
const (
	severityCritical = "critical"
	severityHigh     = "high"
	severityMedium   = "medium"
	severityLow      = "low"
)

var severityOrder = []string{severityCritical, severityHigh, severityMedium, severityLow}

var severityByCategory = map[string]string{
	"security": severityCritical, "data-loss": severityCritical,
	"crash": severityHigh, "correctness": severityHigh, "concurrency": severityHigh,
	"performance": severityMedium, "reliability": severityMedium, "error-handling": severityMedium, "compatibility": severityMedium,
	"maintainability": severityLow, "style": severityLow, "docs": severityLow, "tests": severityLow,
}

var (
	findingVerdicts = map[string]int{"confirmed": 0, "": 1, "likely": 1, "uncertain": 2, "rejected": 3}
	findingOutcomes = map[string]bool{"": true, "open": true, "fixed": true, "wont_fix": true, "deferred": true}
	findingLevels   = map[string]bool{"": true, "quick": true, "standard": true, "deep": true}
)

// Finding is one code review finding, as the model reports it plus what the client derives (ID, Path, Severity).
type Finding struct {
	ID string `json:"id"`
	// File is as reported, relative to the session's cwd when inside it; Path is absolute.
	File string `json:"file"`
	Path string `json:"path"`
	// Line is 1-based; 0 for a finding about the whole file.
	Line            int    `json:"line"`
	Summary         string `json:"summary"`
	FailureScenario string `json:"failure_scenario"`
	Category        string `json:"category,omitempty"`
	Verdict         string `json:"verdict,omitempty"`
	Outcome         string `json:"outcome"`
	ShortSummary    string `json:"short_summary,omitempty"`
	Severity        string `json:"severity"`
}

// findingsState is the session's merged list: a later report updates it, so findings the model does not repeat stay.
type findingsState struct {
	mu    sync.Mutex
	items []Finding
	level string
}

// RestoreFindings loads the list from the last report in the transcript (a resumed session).
func (e *Env) RestoreFindings(raw json.RawMessage) {
	var v struct {
		Findings []Finding `json:"findings"`
		Level    string    `json:"level"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return
	}
	e.findings.mu.Lock()
	e.findings.items, e.findings.level = v.Findings, v.Level
	e.findings.mu.Unlock()
}

// findingSeverity ranks a finding: its category's severity, one step lower when the reviewer is unsure, lowest when
// rejected. An unknown or missing category counts as medium.
func findingSeverity(category, verdict string) string {
	s, ok := severityByCategory[category]
	if !ok {
		s = severityMedium
	}
	switch verdict {
	case "rejected":
		return severityLow
	case "uncertain":
		return severityOrder[min(slices.Index(severityOrder, s)+1, len(severityOrder)-1)]
	}
	return s
}

func findingID(file, summary string) string {
	sum := sha256.Sum256([]byte(file + "\x00" + strings.ToLower(oneLine(summary))))
	return hex.EncodeToString(sum[:6])
}

// mergeFindings applies a report to the list. A finding updates the one with the same file and summary; one with an
// outcome and a reworded summary updates the only finding at the same file and line. Anything else is new.
func mergeFindings(old, report []Finding) []Finding {
	out := append([]Finding(nil), old...)
	for _, f := range report {
		i := slices.IndexFunc(out, func(o Finding) bool { return o.ID == f.ID })
		if i < 0 && f.Outcome != "" && f.Line > 0 {
			var at []int
			for j, o := range out {
				if o.File == f.File && o.Line == f.Line {
					at = append(at, j)
				}
			}
			if len(at) == 1 {
				i = at[0]
			}
		}
		if i < 0 {
			if f.Outcome == "" {
				f.Outcome = "open"
			}
			out = append(out, f)
			continue
		}
		o := out[i]
		f.ID = o.ID
		if f.Outcome == "" {
			f.Outcome = o.Outcome
		}
		if f.Category == "" {
			f.Category = o.Category
		}
		if f.Verdict == "" {
			f.Verdict = o.Verdict
		}
		if f.ShortSummary == "" {
			f.ShortSummary = o.ShortSummary
		}
		out[i] = f
	}
	for i := range out {
		out[i].Severity = findingSeverity(out[i].Category, out[i].Verdict)
	}
	slices.SortStableFunc(out, func(a, b Finding) int {
		if d := slices.Index(severityOrder, a.Severity) - slices.Index(severityOrder, b.Severity); d != 0 {
			return d
		}
		if d := findingVerdicts[a.Verdict] - findingVerdicts[b.Verdict]; d != 0 {
			return d
		}
		if d := strings.Compare(a.File, b.File); d != 0 {
			return d
		}
		return a.Line - b.Line
	})
	return out
}

// ReportFindings records typed code review findings for the frontends to list. It changes nothing but the session's
// list, so it needs no permission.
type ReportFindings struct{}

func (*ReportFindings) Name() string                  { return "ReportFindings" }
func (*ReportFindings) ReadOnly(json.RawMessage) bool { return true }

func (*ReportFindings) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Findings *[]struct {
			File            string `json:"file"`
			Line            int    `json:"line"`
			Summary         string `json:"summary"`
			FailureScenario string `json:"failure_scenario"`
			Category        string `json:"category"`
			Verdict         string `json:"verdict"`
			Outcome         string `json:"outcome"`
			ShortSummary    string `json:"short_summary"`
		} `json:"findings"`
		Level string `json:"level"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.Findings == nil {
		return ErrorResult("InputValidationError: findings is required; pass an empty list when there are none")
	}
	if len(*in.Findings) > maxFindings {
		return ErrorResult("InputValidationError: at most %d findings per call", maxFindings)
	}
	if !findingLevels[in.Level] {
		return ErrorResult("InputValidationError: level must be \"quick\", \"standard\" or \"deep\"")
	}
	report := make([]Finding, 0, len(*in.Findings))
	for i, f := range *in.Findings {
		bad := func(format string, args ...any) Result {
			return ErrorResult("InputValidationError: findings[%d]: "+format, append([]any{i}, args...)...)
		}
		summary, scenario := strings.TrimSpace(f.Summary), strings.TrimSpace(f.FailureScenario)
		category := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(f.Category)), "_", "-")
		switch {
		case strings.TrimSpace(f.File) == "":
			return bad("file is required")
		case f.Line < 0:
			return bad("line numbers start at 1 (0 for the whole file)")
		case summary == "":
			return bad("summary is required")
		case scenario == "":
			return bad("failure_scenario is required")
		case len([]rune(summary)) > maxFindingSummary:
			return bad("summary is longer than %d characters", maxFindingSummary)
		case len([]rune(scenario)) > maxFindingScenario:
			return bad("failure_scenario is longer than %d characters", maxFindingScenario)
		}
		if _, ok := findingVerdicts[f.Verdict]; !ok {
			return bad("verdict must be \"confirmed\", \"likely\", \"uncertain\" or \"rejected\"")
		}
		if !findingOutcomes[f.Outcome] {
			return bad("outcome must be \"open\", \"fixed\", \"wont_fix\" or \"deferred\"")
		}
		path := resolvePath(env.Cwd, strings.TrimSpace(f.File))
		file := path
		if r, err := filepath.Rel(env.Cwd, path); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			file = filepath.ToSlash(r)
		}
		report = append(report, Finding{
			ID: findingID(file, summary), File: file, Path: path, Line: f.Line, Summary: summary, FailureScenario: scenario,
			Category: category, Verdict: f.Verdict, Outcome: f.Outcome, ShortSummary: oneLine(f.ShortSummary),
		})
	}

	st := &env.findings
	st.mu.Lock()
	st.items = mergeFindings(st.items, report)
	if in.Level != "" {
		st.level = in.Level
	}
	items, level := append([]Finding{}, st.items...), st.level
	st.mu.Unlock()

	open := 0
	for _, f := range items {
		if f.Outcome == "open" {
			open++
		}
	}
	shown := false
	if h := env.Host(); h != nil {
		if r, ok := h.Interactor().(HTMLRenderer); ok {
			shown = r.RendersHTML()
		}
	}
	structured := map[string]any{"findings": items, "level": level, "open": open, "reported": len(report), "shown": shown}
	var text string
	switch {
	case len(items) == 0:
		text = "Reported: no findings."
	case len(report) == 0:
		text = fmt.Sprintf("Nothing new reported; the list keeps %d %s, %d open.", len(items), countWord(len(items), "finding", "findings"), open)
	default:
		text = fmt.Sprintf("The list now has %d %s, %d open.", len(items), countWord(len(items), "finding", "findings"), open)
	}
	if shown {
		text += " The user sees it ordered by severity, linked to the files."
	} else {
		text += " No interface that displays the list is attached to this session, so state the findings in your reply."
	}
	r := TextResult(text, structured)
	if len(items) == 0 {
		r.Summary = "No findings"
	} else {
		r.Summary = fmt.Sprintf("%d %s, %d open", len(items), countWord(len(items), "finding", "findings"), open)
	}
	return r
}
