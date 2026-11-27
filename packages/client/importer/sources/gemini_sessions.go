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

// Gemini CLI chat recordings: ~/.gemini/tmp/<project>/chats/session-*.json (one document, the older format) or
// session-*.jsonl (a metadata line, message lines where the latest line of an id wins, {"$set": {...}} and
// {"$rewindTo": id}, which drops that message and everything after it). The project folder is in
// tmp/<project>/.project_root, else in projects.json ({"projects": {path: name}}). Messages of type "info", "error" and
// the like are the CLI's own and are left out, as are thoughts.

type geminiMessage struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Model     string          `json:"model"`
	ToolCalls []struct {
		Name        string          `json:"name"`
		DisplayName string          `json:"displayName"`
		Args        json.RawMessage `json:"args"`
	} `json:"toolCalls"`
}

func geminiChatText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text    string `json:"text"`
		Thought bool   `json:"thought"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// geminiArg is the telling argument of a tool call: a command, a path, a query.
func geminiArg(raw json.RawMessage) string {
	var a map[string]any
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "absolute_path", "path", "query", "pattern", "url"} {
		if s, ok := a[k].(string); ok {
			return s
		}
	}
	return ""
}

// parseGeminiChat reads a chat recording.
func parseGeminiChat(path string) (*importer.Conversation, error) {
	c := &importer.Conversation{}
	var order []string
	byID := map[string]geminiMessage{}
	put := func(m geminiMessage) {
		if m.ID == "" {
			m.ID = fmt.Sprintf("#%d", len(order))
		}
		if _, ok := byID[m.ID]; !ok {
			order = append(order, m.ID)
		}
		byID[m.ID] = m
	}
	meta := func(raw []byte) {
		var m struct {
			SessionID string          `json:"sessionId"`
			StartTime string          `json:"startTime"`
			Messages  []geminiMessage `json:"messages"`
		}
		if json.Unmarshal(raw, &m) == nil {
			c.ID, c.Started = firstNonEmpty(m.SessionID, c.ID), firstNonEmpty(c.Started, m.StartTime)
			for _, msg := range m.Messages {
				put(msg)
			}
		}
	}
	var err error
	if strings.HasSuffix(path, ".json") {
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			meta(data)
		}
	} else {
		err = importer.ReadLines(path, func(line []byte) {
			var rec map[string]json.RawMessage
			if json.Unmarshal(line, &rec) != nil {
				return
			}
			switch {
			case rec["$set"] != nil:
				meta(rec["$set"])
			case rec["$rewindTo"] != nil:
				var id string
				_ = json.Unmarshal(rec["$rewindTo"], &id)
				for i, o := range order {
					if o == id {
						for _, drop := range order[i:] {
							delete(byID, drop)
						}
						order = order[:i]
						break
					}
				}
			case rec["type"] != nil:
				var m geminiMessage
				if json.Unmarshal(line, &m) == nil {
					put(m)
				}
			case rec["sessionId"] != nil:
				meta(line)
			}
		})
	}
	if err != nil {
		return nil, err
	}
	for _, id := range order {
		m := byID[id]
		switch m.Type {
		case "user":
			c.User(geminiChatText(m.Content), m.Timestamp)
		case "gemini", "model":
			c.Model = firstNonEmpty(m.Model, c.Model)
			c.Assistant(geminiChatText(m.Content), m.Timestamp)
			for _, tc := range m.ToolCalls {
				c.Assistant(importer.ToolLine(firstNonEmpty(tc.DisplayName, tc.Name), geminiArg(tc.Args)), m.Timestamp)
			}
		}
	}
	if c.ID == "" {
		return nil, errors.New("no session id: not a chat recording this reader understands")
	}
	return c, nil
}

// geminiProjects maps Gemini CLI's project folder names back to paths.
func geminiProjects(p string) map[string]string {
	out := map[string]string{}
	data, ok, err := readUser(p)
	if !ok || err != nil {
		return out
	}
	var f struct {
		Projects map[string]string `json:"projects"`
	}
	if json.Unmarshal(data, &f) != nil {
		return out
	}
	for path, name := range f.Projects {
		out[name] = path
	}
	return out
}

const geminiSessionNote = "the conversation comes over: prompts, answers and one line per tool call; tool output and thoughts stay behind"

// geminiSessions lists each chat recording as a session to import, newest first.
func geminiSessions(env importer.Env, gemini string) []importer.Found {
	type file struct {
		path, cwd string
		info      os.FileInfo
	}
	tmp := filepath.Join(gemini, "tmp")
	names := geminiProjects(filepath.Join(gemini, "projects.json"))
	var files []file
	projects, _ := os.ReadDir(tmp)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		cwd := ""
		if b, ok, err := readUser(filepath.Join(tmp, p.Name(), ".project_root")); ok && err == nil {
			cwd = strings.TrimSpace(string(b))
		}
		cwd = firstNonEmpty(cwd, names[p.Name()])
		chats, _ := os.ReadDir(filepath.Join(tmp, p.Name(), "chats"))
		for _, ch := range chats {
			n := ch.Name()
			if ch.IsDir() || !strings.HasPrefix(n, "session-") || (!strings.HasSuffix(n, ".json") && !strings.HasSuffix(n, ".jsonl")) {
				continue
			}
			if info, err := ch.Info(); err == nil {
				files = append(files, file{filepath.Join(tmp, p.Name(), "chats", n), cwd, info})
			}
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].info.ModTime().After(files[j].info.ModTime()) })
	var out []importer.Found
	for _, f := range files {
		src := env.Display(f.path)
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f.path), ".jsonl"), ".json")
		if f.info.Size() > importer.MaxSessionBytes {
			out = append(out, importer.Found{Kind: importer.KindSession, Name: name, Source: src, Status: importer.StatusSkipped, Note: "larger than 64 MB"})
			continue
		}
		c, err := parseGeminiChat(f.path)
		if err != nil {
			out = append(out, importer.Found{Kind: importer.KindSession, Name: name, Source: src, Status: importer.StatusError, Note: err.Error()})
			continue
		}
		c.Cwd = f.cwd
		found := importer.Found{Kind: importer.KindSession, Name: firstNonEmpty(c.Title(), c.ID), Source: src, Note: geminiSessionNote}
		switch {
		case c.Cwd == "":
			found.Status, found.Note = importer.StatusSkipped, "the folder it ran in is not recorded"
		case c.Messages() == 0:
			found.Status, found.Note = importer.StatusSkipped, "no messages"
		default:
			hid := importer.ImportedSessionID("gemini", c.ID)
			found.Session = &importer.SessionSource{ID: hid, Cwd: c.Cwd, Modified: f.info.ModTime(), Messages: c.Messages(), Transcript: c.Transcript("gemini", hid)}
		}
		out = append(out, found)
	}
	return out
}
