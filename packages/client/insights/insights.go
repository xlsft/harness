// Package insights reports how the agent was used on this machine, from the transcripts on its disk. Nothing leaves the
// machine and no model is asked: it is counting.
package insights

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// Options select what to read.
type Options struct {
	// Days looks this far back (0: 30).
	Days int
	// Cwd limits it to one folder ("": all).
	Cwd string
	// Dir holds the transcripts (""; the client's projects folder).
	Dir string
	// Now is the time to count back from (zero: now).
	Now time.Time
}

// Count is one row of a ranking.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ModelRow is what one model cost.
type ModelRow struct {
	Model   string  `json:"model"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// Report is the result.
type Report struct {
	Days             int        `json:"days"`
	Sessions         int        `json:"sessions"`
	Prompts          int        `json:"prompts"`
	ModelRequests    int        `json:"model_requests"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
	CostUSD          float64    `json:"cost_usd"`
	ToolCalls        int        `json:"tool_calls"`
	ToolErrors       int        `json:"tool_errors"`
	Compactions      int        `json:"compactions"`
	Agents           int        `json:"agents"`
	Tools            []Count    `json:"tools"`
	Commands         []Count    `json:"commands"`
	Folders          []Count    `json:"folders"`
	Models           []ModelRow `json:"models"`
}

const topRows = 10

// Build reads the transcripts and counts.
func Build(o Options) Report {
	if o.Days <= 0 {
		o.Days = 30
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	dir := o.Dir
	if dir == "" {
		dir = config.ProjectsDir()
	}
	r := Report{Days: o.Days}
	since := o.Now.AddDate(0, 0, -o.Days)
	tools, commands, folders := map[string]int{}, map[string]int{}, map[string]int{}
	models := map[string]*ModelRow{}
	paths, _ := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.ModTime().Before(since) {
			continue
		}
		entries, err := session.Load(p)
		if err != nil || len(entries) == 0 {
			continue
		}
		cwd := ""
		for _, e := range entries {
			if e.Cwd != "" {
				cwd = e.Cwd
				break
			}
		}
		if o.Cwd != "" && cwd != o.Cwd {
			continue
		}
		r.Sessions++
		if cwd != "" {
			folders[cwd]++
		}
		r.add(entries, tools, commands, models)
		// A session's agents keep transcripts of their own beside it.
		agents, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents", "*.jsonl"))
		for _, ap := range agents {
			if ae, err := session.Load(ap); err == nil {
				r.Agents++
				r.add(ae, tools, commands, models)
			}
		}
	}
	r.Tools, r.Commands, r.Folders = top(tools), top(commands), top(folders)
	for _, m := range models {
		r.Models = append(r.Models, *m)
	}
	sort.Slice(r.Models, func(i, j int) bool {
		if r.Models[i].Tokens != r.Models[j].Tokens {
			return r.Models[i].Tokens > r.Models[j].Tokens
		}
		return r.Models[i].Model < r.Models[j].Model
	})
	return r
}

func (r *Report) add(entries []session.Entry, tools, commands map[string]int, models map[string]*ModelRow) {
	seen := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.CompactMetadata != nil {
			r.Compactions++
		}
		switch e.Type {
		case session.TypeUser:
			blocks := e.Blocks()
			prompt := !e.IsMeta
			for _, b := range blocks {
				if b.Type == llm.BlockToolResult {
					prompt = false
					if b.IsError {
						r.ToolErrors++
					}
				}
			}
			if prompt && len(blocks) > 0 {
				r.Prompts++
			}
		case session.TypeAssistant:
			if e.Message == nil {
				continue
			}
			for _, b := range e.Blocks() {
				if b.Type != llm.BlockToolUse {
					continue
				}
				r.ToolCalls++
				tools[b.Name]++
				if b.Name == "Bash" {
					if c := commandHead(b.Input); c != "" {
						commands[c]++
					}
				}
			}
			if e.Message.ID != "" && seen[e.Message.ID] {
				continue
			}
			seen[e.Message.ID] = true
			r.ModelRequests++
			r.CostUSD += e.Message.CostUSD
			m := models[e.Message.Model]
			if m == nil {
				m = &ModelRow{Model: e.Message.Model}
				models[e.Message.Model] = m
			}
			m.CostUSD += e.Message.CostUSD
			if u := e.Message.Usage; u != nil {
				r.InputTokens += int64(u.InputTokens)
				r.OutputTokens += int64(u.OutputTokens)
				r.CacheReadTokens += int64(u.CacheReadInputTokens)
				r.CacheWriteTokens += int64(u.CacheCreationInputTokens)
				m.Tokens += int64(u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens)
			}
		}
	}
}

// commandHead is the first word of a command line and its subcommand when it has one: "git status", not the whole line,
// which may hold anything.
func commandHead(input json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	fields := strings.Fields(in.Command)
	if len(fields) == 0 {
		return ""
	}
	head := filepath.Base(fields[0])
	if len(fields) > 1 && isWord(fields[1]) {
		switch head {
		case "git", "go", "npm", "pnpm", "yarn", "cargo", "docker", "kubectl", "make", "gh", "bun", "uv":
			return head + " " + fields[1]
		}
	}
	return head
}

func isWord(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return s != ""
}

func top(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > topRows {
		out = out[:topRows]
	}
	return out
}

// Text is the report for reading.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Last %d days on this machine: %d sessions, %d prompts, %d agents.\n", r.Days, r.Sessions, r.Prompts, r.Agents)
	if r.Sessions == 0 {
		return strings.TrimRight(b.String(), "\n")
	}
	fmt.Fprintf(&b, "%d model requests: %s in, %s out, %s from cache, %s written to it.\n", r.ModelRequests, Tokens(r.InputTokens), Tokens(r.OutputTokens), Tokens(r.CacheReadTokens), Tokens(r.CacheWriteTokens))
	if r.CostUSD > 0 {
		fmt.Fprintf(&b, "Cost on metered providers: $%.2f. Flat-rate plans count as tokens only.\n", r.CostUSD)
	}
	if total := r.CacheReadTokens + r.InputTokens + r.CacheWriteTokens; total > 0 {
		fmt.Fprintf(&b, "%d%% of the input came from the prompt cache.\n", r.CacheReadTokens*100/total)
	}
	fmt.Fprintf(&b, "%d tool calls, %d failed. %d compactions.\n", r.ToolCalls, r.ToolErrors, r.Compactions)
	section := func(title string, rows []Count) {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n", title)
		for _, c := range rows {
			fmt.Fprintf(&b, "  %5d  %s\n", c.Count, c.Name)
		}
	}
	section("Tools", r.Tools)
	section("Commands", r.Commands)
	section("Folders", r.Folders)
	if len(r.Models) > 0 {
		b.WriteString("\nModels:\n")
		for _, m := range r.Models {
			fmt.Fprintf(&b, "  %8s  $%-8.2f %s\n", Tokens(m.Tokens), m.CostUSD, m.Model)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Tokens formats a token count.
func Tokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
