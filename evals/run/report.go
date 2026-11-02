package main

import (
	"encoding/json"
	"fmt"
	"github.com/xlsft/harness/evals/task"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Run statuses.
const (
	StatusPass    = "pass"
	StatusFail    = "fail"    // the agent finished but check.sh failed
	StatusTimeout = "timeout" // the task timeout killed the client
	StatusError   = "error"   // infrastructure problem: workdir, client start, check could not run
)

// RunResult is one model x task x repeat attempt.
type RunResult struct {
	Model      string  `json:"model"`
	Task       string  `json:"task"`
	Repeat     int     `json:"repeat"`
	Status     string  `json:"status"`
	Passed     bool    `json:"passed"`
	Parsed     bool    `json:"parsed"` // the client's JSON result was found
	Subtype    string  `json:"subtype,omitempty"`
	IsError    bool    `json:"is_error"`
	Turns      int     `json:"turns"`
	Usage      Usage   `json:"usage"`
	CostUSD    float64 `json:"cost_usd"`
	DurationMS int64   `json:"duration_ms"`
	ClientExit int     `json:"client_exit"`
	TimedOut   bool    `json:"timed_out"`
	Error      string  `json:"error,omitempty"`
	LogDir     string  `json:"log_dir"`
	Workdir    string  `json:"workdir,omitempty"`
}

// ModelSummary aggregates all attempts of one model.
type ModelSummary struct {
	Model         string  `json:"model"`
	Runs          int     `json:"runs"`
	Passed        int     `json:"passed"`
	PassRate      float64 `json:"pass_rate"`
	Timeouts      int     `json:"timeouts"`
	Errors        int     `json:"errors"`
	AvgTurns      float64 `json:"avg_turns"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	CacheTokens   int64   `json:"cache_tokens"`
	AvgTokens     float64 `json:"avg_tokens"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	AvgCostUSD    float64 `json:"avg_cost_usd"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

// Report is the full output written to results.json.
type Report struct {
	StartedAt time.Time      `json:"started_at"`
	Duration  string         `json:"duration"`
	Client    string         `json:"client"`
	Server    string         `json:"server,omitempty"`
	Repeat    int            `json:"repeat"`
	Tasks     []*task.Task   `json:"tasks"`
	Summary   []ModelSummary `json:"summary"`
	Runs      []RunResult    `json:"runs"`
}

func buildReport(cfg *config, tasks []*task.Task, runs []RunResult, started time.Time) *Report {
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		return a.Repeat < b.Repeat
	})
	rep := &Report{
		StartedAt: started.UTC(), Duration: time.Since(started).Round(time.Second).String(),
		Client: cfg.client, Server: cfg.server, Repeat: cfg.repeat, Tasks: tasks, Runs: runs,
	}
	for _, m := range cfg.models {
		s := ModelSummary{Model: m}
		var turnsN int
		var turns, dur int64
		for _, r := range runs {
			if r.Model != m {
				continue
			}
			s.Runs++
			if r.Passed {
				s.Passed++
			}
			switch r.Status {
			case StatusTimeout:
				s.Timeouts++
			case StatusError:
				s.Errors++
			}
			if r.Parsed {
				turnsN++
				turns += int64(r.Turns)
			}
			s.InputTokens += r.Usage.InputTokens
			s.OutputTokens += r.Usage.OutputTokens
			s.CacheTokens += r.Usage.CacheReadInputTokens + r.Usage.CacheCreationInputTokens
			s.TotalCostUSD += r.CostUSD
			dur += r.DurationMS
		}
		if s.Runs > 0 {
			s.PassRate = float64(s.Passed) / float64(s.Runs)
			s.AvgTokens = float64(s.InputTokens+s.OutputTokens+s.CacheTokens) / float64(s.Runs)
			s.AvgCostUSD = s.TotalCostUSD / float64(s.Runs)
			s.AvgDurationMS = float64(dur) / float64(s.Runs)
		}
		if turnsN > 0 {
			s.AvgTurns = float64(turns) / float64(turnsN)
		}
		rep.Summary = append(rep.Summary, s)
	}
	return rep
}

// Markdown renders the summary table and a task x model pass matrix.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# harness eval results\n\n")
	fmt.Fprintf(&b, "Started %s, took %s, %d repeat(s) per task.\n\n", r.StartedAt.Format(time.RFC3339), r.Duration, r.Repeat)
	b.WriteString("| Model | Pass rate | Passed | Timeouts | Errors | Avg turns | Avg tokens | Input | Output | Cache | Cost (USD) | Avg time |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, s := range r.Summary {
		fmt.Fprintf(&b, "| %s | %.0f%% | %d/%d | %d | %d | %.1f | %s | %s | %s | %s | %.4f | %s |\n",
			s.Model, 100*s.PassRate, s.Passed, s.Runs, s.Timeouts, s.Errors, s.AvgTurns,
			humanInt(int64(s.AvgTokens)), humanInt(s.InputTokens), humanInt(s.OutputTokens), humanInt(s.CacheTokens),
			s.TotalCostUSD, (time.Duration(s.AvgDurationMS) * time.Millisecond).Round(100*time.Millisecond))
	}

	b.WriteString("\n## Per task\n\n| Task | Tags |")
	for _, s := range r.Summary {
		fmt.Fprintf(&b, " %s |", s.Model)
	}
	b.WriteString("\n|---|---|")
	for range r.Summary {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	for _, t := range r.Tasks {
		fmt.Fprintf(&b, "| %s | %s |", t.ID, strings.Join(t.Tags, ", "))
		for _, s := range r.Summary {
			pass, n, turns := 0, 0, 0
			for _, run := range r.Runs {
				if run.Model == s.Model && run.Task == t.ID {
					n++
					turns += run.Turns
					if run.Passed {
						pass++
					}
				}
			}
			if n == 0 {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(&b, " %d/%d (%.1f turns) |", pass, n, float64(turns)/float64(n))
		}
		b.WriteString("\n")
	}

	var failed []RunResult
	for _, run := range r.Runs {
		if !run.Passed {
			failed = append(failed, run)
		}
	}
	if len(failed) > 0 {
		b.WriteString("\n## Failed attempts\n\n| Model | Task | # | Status | Turns | Detail |\n|---|---|---:|---|---:|---|\n")
		for _, f := range failed {
			detail := f.Error
			if detail == "" {
				detail = "check.sh failed, see " + filepath.Join(f.LogDir, "check.log")
			}
			detail = strings.ReplaceAll(strings.ReplaceAll(detail, "|", `\|`), "\n", " ")
			fmt.Fprintf(&b, "| %s | %s | %d | %s | %d | %s |\n", f.Model, f.Task, f.Repeat, f.Status, f.Turns, detail)
		}
	}
	return b.String()
}

func humanInt(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func writeReport(dir string, r *Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "results.md"), []byte(r.Markdown()), 0o644)
}
