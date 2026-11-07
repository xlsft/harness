package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/tools"
)

// Search limits.
const (
	defaultSearchResults = 5
	maxSearchResults     = 50
)

// Tool is ToolSearch: it loads deferred tools, built-in and MCP alike. It returns their full definitions and makes
// them part of every request from the next one on. Queries are "select:<name>[,<name>...]" for exact names, or
// keywords matched against tool names and descriptions ("+word" requires word in the name).
type Tool struct {
	L *Loader
}

var _ tools.Tool = (*Tool)(nil)

type searchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// Name implements tools.Tool.
func (s *Tool) Name() string { return Name }

// ReadOnly implements tools.Tool.
func (s *Tool) ReadOnly(json.RawMessage) bool { return true }

// Run implements tools.Tool.
func (s *Tool) Run(_ context.Context, _ *tools.Env, call tools.Call) tools.Result {
	var in searchInput
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return tools.ErrorResult("InputValidationError: %v", err)
		}
	}
	if strings.TrimSpace(in.Query) == "" {
		return tools.ErrorResult("InputValidationError: query is required")
	}
	names := s.L.find(in)
	total := len(s.L.Deferred())
	if len(names) == 0 {
		return tools.Result{
			Content:    tools.TextResult(fmt.Sprintf("No deferred tools match %q (%d deferred tools in total). Try other keywords or exact names with select:.", in.Query, total), nil).Content,
			Structured: map[string]any{"query": in.Query, "matches": []string{}, "total_deferred_tools": total},
			Summary:    "No matches",
		}
	}
	s.L.Load(names...)
	var defs []llm.ToolDef
	for _, n := range names {
		if d, ok := s.L.Def(n); ok {
			defs = append(defs, d)
		}
	}
	return tools.Result{
		Content:    tools.TextResult(fmt.Sprintf("Loaded %d tool(s); they can be called from now on:\n%s", len(defs), Functions(defs)), nil).Content,
		Structured: map[string]any{"query": in.Query, "matches": names, "total_deferred_tools": total},
		Summary:    fmt.Sprintf("Loaded %d tool(s)", len(defs)),
	}
}

// Functions renders definitions as the <functions> block ToolSearch returns.
func Functions(defs []llm.ToolDef) string {
	var b strings.Builder
	b.WriteString("<functions>\n")
	for _, d := range defs {
		line, _ := json.Marshal(map[string]any{"name": d.Name, "description": d.Description, "parameters": json.RawMessage(d.InputSchema)})
		b.WriteString("<function>")
		b.Write(line)
		b.WriteString("</function>\n")
	}
	b.WriteString("</functions>")
	return b.String()
}

// find resolves a query to deferred tool names (no side effects).
func (l *Loader) find(in searchInput) []string {
	defs := l.Deferred()
	q := strings.TrimSpace(in.Query)
	if rest, ok := strings.CutPrefix(q, "select:"); ok {
		var out []string
		seen := map[string]bool{}
		for _, part := range strings.Split(rest, ",") {
			if n := resolve(defs, strings.TrimSpace(part)); n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		return out
	}
	if n := resolve(defs, q); n != "" {
		return []string{n}
	}
	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultSearchResults
	}
	limit = min(limit, maxSearchResults)
	return keyword(defs, q, limit)
}

// resolve finds a deferred tool by exact name, case-insensitive name, or a unique "<tool>" / "<server>__<tool>"
// suffix of an MCP tool.
func resolve(defs []llm.ToolDef, name string) string {
	if name == "" {
		return ""
	}
	var ci, suffix []string
	for _, d := range defs {
		switch {
		case d.Name == name:
			return name
		case strings.EqualFold(d.Name, name):
			ci = append(ci, d.Name)
		case strings.HasSuffix(strings.ToLower(d.Name), "__"+strings.ToLower(name)):
			suffix = append(suffix, d.Name)
		}
	}
	if len(ci) == 1 {
		return ci[0]
	}
	if len(suffix) == 1 {
		return suffix[0]
	}
	return ""
}

// nameParts splits a tool name into lower-case words: at "_" and "-" (MCP names) and at case changes (built-in
// names such as BrowserNavigate).
func nameParts(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-':
			flush()
			continue
		case i > 0 && r >= 'A' && r <= 'Z' && runes[i-1] >= 'a' && runes[i-1] <= 'z':
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// keyword ranks deferred tools by how well their name and description match the terms. Required ("+word") terms
// filter on the tool name; the other terms rank.
func keyword(defs []llm.ToolDef, q string, limit int) []string {
	var required, terms []string
	for _, f := range strings.Fields(strings.ToLower(q)) {
		if r, ok := strings.CutPrefix(f, "+"); ok {
			if r != "" {
				required = append(required, r)
			}
			continue
		}
		terms = append(terms, f)
	}
	if len(terms) == 0 {
		// Only required words: rank by them.
		terms = required
	}
	type hit struct {
		name  string
		score int
	}
	var hits []hit
	for _, d := range defs {
		lname := strings.ToLower(d.Name)
		ok := true
		for _, r := range required {
			if !strings.Contains(lname, r) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		parts := nameParts(d.Name)
		desc := strings.ToLower(d.Description)
		score := 0
		for _, term := range terms {
			switch {
			case contains(parts, term):
				score += 10
			case strings.Contains(lname, term):
				score += 5
			}
			if strings.Contains(desc, term) {
				score += 2
			}
		}
		if score > 0 {
			hits = append(hits, hit{d.Name, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	var out []string
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].name)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
