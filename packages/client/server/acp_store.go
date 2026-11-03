package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// ACP sessions are recorded by the client itself (the agent keeps its own transcript): HARNESS_HOME/acp/sessions/
// <session id>/meta.json (engine, agent, the agent's session id, folder, title) and events.jsonl (the durable
// session events, replayed when the session is resumed). Nothing of it is uploaded to the harness server.

// acpMeta is what is recorded about an ACP session.
type acpMeta struct {
	ID             string    `json:"id"`
	Engine         string    `json:"engine"`
	AgentID        string    `json:"agentId"`
	AgentName      string    `json:"agentName"`
	AgentVersion   string    `json:"agentVersion,omitempty"`
	AgentSessionID string    `json:"agentSessionId,omitempty"`
	Cwd            string    `json:"cwd"`
	NoFolder       bool      `json:"noFolder,omitempty"`
	Title          string    `json:"title,omitempty"`
	CustomTitle    bool      `json:"customTitle,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func acpSessionsDir() string { return filepath.Join(config.Home(), "acp", "sessions") }

func acpSessionDir(id string) string { return filepath.Join(acpSessionsDir(), id) }

// readAcpMeta returns the record of an ACP session (nil when id is not one).
func readAcpMeta(id string) *acpMeta {
	if !validSessionID(id) {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(acpSessionDir(id), "meta.json"))
	if err != nil {
		return nil
	}
	var m acpMeta
	if json.Unmarshal(b, &m) != nil || m.ID != id {
		return nil
	}
	return &m
}

func writeAcpMeta(m acpMeta) error {
	dir := acpSessionDir(m.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".meta.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "meta.json"))
}

// listAcpMeta returns the recorded ACP sessions, most recent first.
func listAcpMeta() []acpMeta {
	entries, _ := os.ReadDir(acpSessionsDir())
	var out []acpMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if m := readAcpMeta(e.Name()); m != nil {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// acpLog appends durable events to events.jsonl.
type acpLog struct {
	mu sync.Mutex
	f  *os.File
}

func openAcpLog(id string) (*acpLog, error) {
	dir := acpSessionDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &acpLog{f: f}, nil
}

// persisted says whether an event goes to the record: the conversation, not session state or prompts.
func persisted(ev *agentv1.SessionEvent) bool {
	switch ev.GetEvent().(type) {
	case *agentv1.SessionEvent_UserMessage, *agentv1.SessionEvent_AssistantMessage, *agentv1.SessionEvent_ToolCallStarted,
		*agentv1.SessionEvent_ToolCallFinished, *agentv1.SessionEvent_TurnStarted, *agentv1.SessionEvent_TurnFinished,
		*agentv1.SessionEvent_TodosUpdated, *agentv1.SessionEvent_PermissionModeChanged:
		return true
	case *agentv1.SessionEvent_Notice:
		return strings.HasPrefix(ev.GetNotice().GetKind(), "acp_fresh")
	}
	return false
}

func (l *acpLog) append(ev *agentv1.SessionEvent) {
	if l == nil || !persisted(ev) {
		return
	}
	b, err := protojson.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_, _ = l.f.Write(append(b, '\n'))
	}
}

func (l *acpLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// maxAcpReplay caps the events replayed on resume (the most recent ones).
const maxAcpReplay = 5000

// readAcpEvents returns the recorded events of a session for replay: seqs cleared, and tool calls and turns
// that never finished (the client stopped mid-turn) closed as interrupted.
func readAcpEvents(id string) []*agentv1.SessionEvent {
	f, err := os.Open(filepath.Join(acpSessionDir(id), "events.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []*agentv1.SessionEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var ev agentv1.SessionEvent
		if protojson.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		ev.Seq, ev.SessionId = 0, ""
		out = append(out, &ev)
	}
	if len(out) > maxAcpReplay {
		out = out[len(out)-maxAcpReplay:]
	}
	open := map[string]bool{}
	var order []string
	turn := false
	for _, ev := range out {
		switch e := ev.GetEvent().(type) {
		case *agentv1.SessionEvent_ToolCallStarted:
			open[e.ToolCallStarted.GetToolUseId()] = true
			order = append(order, e.ToolCallStarted.GetToolUseId())
		case *agentv1.SessionEvent_ToolCallFinished:
			delete(open, e.ToolCallFinished.GetToolUseId())
		case *agentv1.SessionEvent_TurnStarted:
			turn = true
		case *agentv1.SessionEvent_TurnFinished:
			turn = false
		}
	}
	for _, id := range order {
		if open[id] {
			out = append(out, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{ToolUseId: id, Interrupted: true, Summary: "interrupted"}}})
		}
	}
	if turn {
		out = append(out, &agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{Result: agentv1.TurnFinished_RESULT_INTERRUPTED}}})
	}
	return out
}
