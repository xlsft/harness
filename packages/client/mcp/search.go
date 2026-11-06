package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/tools"
)

// Search limits.
const (
	defaultSearchResults = 5
	maxSearchResults     = 50
)

// ToolSearch loads deferred tools: it returns their full definitions and makes them callable from
// the next model request on. Queries are "select:<name>[,<name>...]" for exact names, or keywords
// matched against tool names and descriptions ("+word" requires word in the name).
type ToolSearch struct {
	m *Manager
}

var _ tools.Tool = (*ToolSearch)(nil)

type searchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// Name implements tools.Tool.
func (s *ToolSearch) Name() string { return ToolSearchName }

// ReadOnly implements tools.Tool.
func (s *ToolSearch) ReadOnly(json.RawMessage) bool { return true }

// Run implements tools.Tool.
func (s *ToolSearch) Run(_ context.Context, _ *tools.Env, call tools.Call) tools.Result {
	var in searchInput
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return tools.ErrorResult("InputValidationError: %v", err)
		}
	}
	if strings.TrimSpace(in.Query) == "" {
		return tools.ErrorResult("InputValidationError: query is required")
	}
	names := s.find(in)
	total := len(s.m.Deferred())
	if len(names) == 0 {
		return tools.Result{
			Content:    tools.TextResult(fmt.Sprintf("No deferred tools match %q (%d deferred tools in total). Try other keywords or exact names with select:.", in.Query, total), nil).Content,
			Structured: map[string]any{"query": in.Query, "matches": []string{}, "total_deferred_tools": total},
			Summary:    "No matches",
		}
	}
	s.m.load(names...)
	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d tool(s); they can be called from now on:\n<functions>\n", len(names))
	for _, n := range names {
		d := s.m.lookup(n).def
		line, _ := json.Marshal(map[string]any{"name": d.Name, "description": d.Description, "parameters": json.RawMessage(d.InputSchema)})
		b.WriteString("<function>")
		b.Write(line)
		b.WriteString("</function>\n")
	}
	b.WriteString("</functions>")
	return tools.Result{
		Content:    tools.TextResult(b.String(), nil).Content,
		Structured: map[string]any{"query": in.Query, "matches": names, "total_deferred_tools": total},
		Summary:    fmt.Sprintf("Loaded %d tool(s)", len(names)),
	}
}

// find resolves a query to deferred tool names (no side effects).
func (s *ToolSearch) find(in searchInput) []string {
	q := strings.TrimSpace(in.Query)
	if rest, ok := strings.CutPrefix(q, "select:"); ok {
		var out []string
		seen := map[string]bool{}
		for _, part := range strings.Split(rest, ",") {
			if n := s.resolve(strings.TrimSpace(part)); n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		return out
	}
	if n := s.resolve(q); n != "" {
		return []string{n}
	}
	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultSearchResults
	}
	limit = min(limit, maxSearchResults)
	return s.keyword(q, limit)
}

// resolve finds a deferred tool by exact name, case-insensitive name, or a unique "<tool>" /
// "<server>__<tool>" suffix.
func (s *ToolSearch) resolve(name string) string {
	if name == "" {
		return ""
	}
	if t := s.m.lookup(name); t != nil && t.deferred {
		return name
	}
	var ci, suffix []string
	for _, t := range s.m.toolList() {
		if !t.deferred || s.m.disabled(t.name) {
			continue
		}
		switch {
		case strings.EqualFold(t.name, name):
			ci = append(ci, t.name)
		case strings.HasSuffix(strings.ToLower(t.name), "__"+strings.ToLower(name)):
			suffix = append(suffix, t.name)
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

func nameParts(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' })
}

// keyword ranks deferred tools by how well their name and description match the terms. Required
// ("+word") terms filter on the tool name; the other terms rank.
func (s *ToolSearch) keyword(q string, limit int) []string {
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
	for _, t := range s.m.toolList() {
		if !t.deferred || s.m.disabled(t.name) {
			continue
		}
		lname := strings.ToLower(t.name)
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
		parts := nameParts(t.name)
		desc := strings.ToLower(t.def.Description)
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
			hits = append(hits, hit{t.name, score})
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
