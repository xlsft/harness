package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// Sessions of other tools, converted into xlsft`s harness's transcript format. What comes over is the conversation:
// the user's prompts and the model's answers, with one line for each tool call. Tool output, reasoning and token
// counts stay behind: their shapes are each tool's own and change between versions, and an answer reads the same
// without them.
//
// An imported session has a fixed id derived from the tool's, and its transcript is the same bytes on every run, so a
// second import finds it and can tell whether it was continued here.

// MaxSessionBytes is the largest session file a reader converts.
const MaxSessionBytes = 64 << 20

// importNamespace derives the imported sessions' ids.
var importNamespace = uuid.MustParse("5c1f6f8e-3b0e-4f43-9c55-2f0a8d7f4a10")

// SessionSource is a session another tool recorded, converted. Readers set it on a KindSession Found.
type SessionSource struct {
	// ID is the harness session id (ImportedSessionID of the tool's).
	ID string
	// Cwd is the folder it ran in; the transcript goes where a session of that folder would.
	Cwd      string
	Modified time.Time
	Messages int
	// Transcript is the harness JSONL.
	Transcript []byte
}

// ImportedSessionID is the id a session of tool with the tool's id gets here.
func ImportedSessionID(tool, id string) string {
	return uuid.NewSHA1(importNamespace, []byte(tool+":"+id)).String()
}

type convMsg struct {
	role, text, ts string
}

// Conversation collects a session's messages as a reader parses it.
type Conversation struct {
	ID, Cwd, Branch, Model, Started string
	msgs                            []convMsg
}

// User adds a prompt.
func (c *Conversation) User(text, ts string) {
	if text = strings.TrimSpace(text); text != "" {
		c.msgs = append(c.msgs, convMsg{role: llm.RoleUser, text: text, ts: ts})
	}
}

// Assistant adds text to the model's turn: consecutive parts (answers and tool lines) make one message.
func (c *Conversation) Assistant(text, ts string) {
	if text = strings.TrimSpace(text); text == "" {
		return
	}
	if n := len(c.msgs); n > 0 && c.msgs[n-1].role == llm.RoleAssistant {
		c.msgs[n-1].text += "\n\n" + text
		return
	}
	c.msgs = append(c.msgs, convMsg{role: llm.RoleAssistant, text: text, ts: ts})
}

// Messages is how many messages there are.
func (c *Conversation) Messages() int { return len(c.msgs) }

// Title is the first prompt, shortened.
func (c *Conversation) Title() string {
	for _, m := range c.msgs {
		if m.role == llm.RoleUser {
			return shortTitle(m.text)
		}
	}
	return ""
}

// Transcript renders the conversation as JSONL entries under the harness id hid, the same bytes on every run.
func (c *Conversation) Transcript(tool, hid string) []byte {
	var buf bytes.Buffer
	var parent *string
	last := c.Started
	for i, m := range c.msgs {
		ts := firstNonEmpty(m.ts, last)
		if ts == "" {
			ts = "1970-01-01T00:00:00.000Z"
		}
		last = ts
		id := uuid.NewSHA1(importNamespace, []byte(fmt.Sprintf("%s:%d", hid, i))).String()
		e := session.Entry{ParentUUID: parent, Type: m.role, SessionID: hid, UUID: id, Timestamp: ts, Cwd: c.Cwd, GitBranch: c.Branch,
			UserType: "external", Version: "import:" + tool, Entrypoint: "import"}
		if m.role == llm.RoleUser {
			e.Message = session.UserText(m.text)
		} else {
			raw, _ := json.Marshal([]llm.ContentBlock{{Type: "text", Text: m.text}})
			e.Message = &session.Message{ID: "import-" + id, Type: "message", Role: llm.RoleAssistant, Model: c.Model, Content: raw}
		}
		line, _ := json.Marshal(e)
		buf.Write(line)
		buf.WriteByte('\n')
		p := id
		parent = &p
	}
	return buf.Bytes()
}

// ReadLines calls fn for each non-empty line of a file, however long, up to MaxSessionBytes.
func ReadLines(path string, fn func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(io.LimitReader(f, MaxSessionBytes), 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// ToolLine is the line a tool call becomes: "*Used shell: `ls`*".
func ToolLine(name, detail string) string {
	detail = strings.Join(strings.Fields(detail), " ")
	if r := []rune(detail); len(r) > 200 {
		detail = string(r[:199]) + "…"
	}
	if detail == "" {
		return "*Used " + name + "*"
	}
	return "*Used " + name + ": `" + strings.ReplaceAll(detail, "`", "'") + "`*"
}

// writeTranscript puts data at dst through a temporary file, with the source's modification time.
func writeTranscript(dst string, data []byte, mod time.Time) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".import-tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if !mod.IsZero() {
		_ = os.Chtimes(tmp, mod, mod)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
