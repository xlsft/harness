package sources

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xlsft/harness/packages/client/importer"
)

// Codex rollouts (codex-rs/rollout, codex-rs/protocol/src/models.rs): sessions/YYYY/MM/DD/rollout-<time>-<uuid>.jsonl and
// archived_sessions/, lines of {timestamp, type, payload}; files from before about August 2025 have a bare meta object
// first and bare response items after it. Cold rollouts compressed to .jsonl.zst are counted and not read. Threads a
// subagent ran are left out: they belong to the session that started them.

// codexContextPrefixes start the messages Codex adds in the user's name: its environment and the instruction files.
var codexContextPrefixes = []string{"<environment_context>", "<user_instructions>", "<turn_aborted>", "# AGENTS.md instructions", "<user_shell_command>"}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   []codexContent  `json:"content"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Action    json.RawMessage `json:"action"`
}

// errSubagent marks a rollout of a subagent's thread.
var errSubagent = errors.New("a subagent's thread: it is part of the session that started it")

// parseCodexRollout reads a rollout file.
func parseCodexRollout(path string) (*importer.Conversation, error) {
	c := &importer.Conversation{}
	first, sub := true, false
	err := importer.ReadLines(path, func(line []byte) {
		var env struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
			ID        string          `json:"id"`
		}
		if json.Unmarshal(line, &env) != nil {
			return
		}
		isFirst := first
		first = false
		if len(env.Payload) == 0 {
			if isFirst && env.ID != "" {
				sub = codexMeta(c, line) || sub
				return
			}
			var it codexItem
			if json.Unmarshal(line, &it) == nil {
				codexResponse(c, it, env.Timestamp)
			}
			return
		}
		switch env.Type {
		case "session_meta":
			sub = codexMeta(c, env.Payload) || sub
		case "turn_context":
			var tc struct {
				Model string `json:"model"`
				Cwd   string `json:"cwd"`
			}
			if json.Unmarshal(env.Payload, &tc) == nil {
				c.Model = firstNonEmpty(tc.Model, c.Model)
				c.Cwd = firstNonEmpty(c.Cwd, tc.Cwd)
			}
		case "response_item":
			var it codexItem
			if json.Unmarshal(env.Payload, &it) == nil {
				codexResponse(c, it, env.Timestamp)
			}
		}
	})
	switch {
	case err != nil:
		return nil, err
	case sub:
		return nil, errSubagent
	case c.ID == "":
		return nil, errors.New("no session meta: not a rollout this reader understands")
	}
	return c, nil
}

// codexMeta reads session_meta; true for a subagent's thread.
func codexMeta(c *importer.Conversation, raw []byte) bool {
	var m struct {
		ID        string          `json:"id"`
		Timestamp string          `json:"timestamp"`
		Cwd       string          `json:"cwd"`
		Source    json.RawMessage `json:"source"`
		Git       *struct {
			Branch string `json:"branch"`
		} `json:"git"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	c.ID, c.Cwd, c.Started = firstNonEmpty(c.ID, m.ID), firstNonEmpty(c.Cwd, m.Cwd), firstNonEmpty(c.Started, m.Timestamp)
	if m.Git != nil {
		c.Branch = m.Git.Branch
	}
	var src map[string]json.RawMessage
	return json.Unmarshal(m.Source, &src) == nil && src["subagent"] != nil
}

func codexResponse(c *importer.Conversation, it codexItem, ts string) {
	switch it.Type {
	case "message":
		var parts []string
		for _, ct := range it.Content {
			if ct.Text != "" && (ct.Type == "input_text" || ct.Type == "output_text" || ct.Type == "text") {
				parts = append(parts, ct.Text)
			}
		}
		text := strings.Join(parts, "\n")
		switch it.Role {
		case "user":
			for _, p := range codexContextPrefixes {
				if strings.HasPrefix(strings.TrimSpace(text), p) {
					return
				}
			}
			c.User(text, ts)
		case "assistant":
			c.Assistant(text, ts)
		}
	case "function_call":
		c.Assistant(importer.ToolLine(it.Name, codexCommand(it.Arguments)), ts)
	case "local_shell_call":
		var a struct {
			Command []string `json:"command"`
		}
		_ = json.Unmarshal(it.Action, &a)
		c.Assistant(importer.ToolLine("shell", strings.Join(a.Command, " ")), ts)
	case "custom_tool_call":
		c.Assistant(importer.ToolLine(it.Name, ""), ts)
	case "web_search_call":
		var a struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(it.Action, &a)
		c.Assistant(importer.ToolLine("web search", a.Query), ts)
	}
}

// codexCommand is the command line of a shell call's arguments, "" for another tool.
func codexCommand(args string) string {
	var a struct {
		Command any    `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	switch v := a.Command.(type) {
	case []any:
		var parts []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				parts = append(parts, s)
			}
		}
		// ["bash", "-lc", "…"] is the command itself.
		if len(parts) == 3 && (parts[1] == "-lc" || parts[1] == "-c") {
			return parts[2]
		}
		return strings.Join(parts, " ")
	case string:
		return v
	}
	return a.Cmd
}

const codexSessionNote = "the conversation comes over: prompts, answers and one line per tool call; tool output and reasoning stay behind"

// codexSessions lists each rollout as a session to import, newest first.
func codexSessions(env importer.Env, codexHome string) []importer.Found {
	type file struct {
		path string
		info os.FileInfo
	}
	var files []file
	packed := 0
	for _, sub := range []string{"sessions", "archived_sessions"} {
		dir := filepath.Join(codexHome, sub)
		for _, rel := range userFiles(dir, ".jsonl", true) {
			if strings.HasPrefix(filepath.Base(rel), "rollout-") {
				p := filepath.Join(dir, rel)
				if info, err := os.Stat(p); err == nil {
					files = append(files, file{p, info})
				}
			}
		}
		for _, rel := range userFiles(dir, ".jsonl.zst", true) {
			if strings.HasPrefix(filepath.Base(rel), "rollout-") {
				packed++
			}
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].info.ModTime().After(files[j].info.ModTime()) })
	var out []importer.Found
	for _, f := range files {
		src := env.Display(f.path)
		name := strings.TrimSuffix(filepath.Base(f.path), ".jsonl")
		if f.info.Size() > importer.MaxSessionBytes {
			out = append(out, importer.Found{Kind: importer.KindSession, Name: name, Source: src, Status: importer.StatusSkipped, Note: "larger than 64 MB"})
			continue
		}
		c, err := parseCodexRollout(f.path)
		switch {
		case errors.Is(err, errSubagent):
			out = append(out, importer.Found{Kind: importer.KindSession, Name: name, Source: src, Status: importer.StatusSkipped, Note: err.Error()})
			continue
		case err != nil:
			out = append(out, importer.Found{Kind: importer.KindSession, Name: name, Source: src, Status: importer.StatusError, Note: err.Error()})
			continue
		}
		name = firstNonEmpty(c.Title(), c.ID)
		found := importer.Found{Kind: importer.KindSession, Name: name, Source: src, Note: codexSessionNote}
		switch {
		case c.Cwd == "":
			found.Status, found.Note = importer.StatusSkipped, "the folder it ran in is not recorded"
		case c.Messages() == 0:
			found.Status, found.Note = importer.StatusSkipped, "no messages"
		default:
			hid := importer.ImportedSessionID("codex", c.ID)
			found.Session = &importer.SessionSource{ID: hid, Cwd: c.Cwd, Modified: f.info.ModTime(), Messages: c.Messages(), Transcript: c.Transcript("codex", hid)}
		}
		out = append(out, found)
	}
	if packed > 0 {
		out = append(out, noted(importer.KindSession, "Compressed Codex sessions", env.Display(filepath.Join(codexHome, "sessions")),
			fmt.Sprintf("%d older session(s) compressed by Codex (.jsonl.zst): not read", packed)))
	}
	return out
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
