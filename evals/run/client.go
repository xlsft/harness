package main

import (
	"bytes"
	"encoding/json"
	"github.com/xlsft/harness/evals/task"
	"strconv"
	"strings"
)

// clientArgs builds the harness-client command line for one run.
//
// This is the single place that knows the headless CLI contract. Adjust it here
// if the flags of `harness-client` change.
func clientArgs(cfg *config, model string, t *task.Task) []string {
	args := []string{
		"-p", t.Prompt,
		"--model", model,
		"--output-format", "json",
		"--max-turns", strconv.Itoa(t.MaxTurns),
		"--permission-mode", "bypassPermissions",
		"--no-session-persistence",
	}
	if cfg.server != "" {
		args = append(args, "--server", cfg.server)
	}
	return append(args, cfg.extraArgs...)
}

// Usage mirrors the token counters of the client's JSON result.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// Total is every token billed for the run.
func (u Usage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// clientResult is the subset of the `--output-format json` result object the
// runner cares about. Missing fields stay zero.
type clientResult struct {
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype"`
	IsError      bool     `json:"is_error"`
	NumTurns     int      `json:"num_turns"`
	Result       string   `json:"result"`
	Errors       []string `json:"errors"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	CostUSD      *float64 `json:"cost_usd"`
	Usage        Usage    `json:"usage"`
}

func (r *clientResult) cost() float64 {
	switch {
	case r.TotalCostUSD != nil:
		return *r.TotalCostUSD
	case r.CostUSD != nil:
		return *r.CostUSD
	}
	return 0
}

// parseClientOutput finds the result object in the client's stdout. The json
// format prints a single object; to be tolerant of extra lines (or of
// stream-json), the last line that decodes to an object with "type":"result"
// wins, falling back to the whole output decoded as one object.
func parseClientOutput(stdout []byte) (*clientResult, bool) {
	lines := bytes.Split(bytes.TrimSpace(stdout), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(string(lines[i]))
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r clientResult
		if json.Unmarshal([]byte(line), &r) == nil && r.Type == "result" {
			return &r, true
		}
	}
	var r clientResult
	if json.Unmarshal(bytes.TrimSpace(stdout), &r) == nil && (r.Type == "result" || r.NumTurns > 0 || r.Result != "") {
		return &r, true
	}
	return nil, false
}
