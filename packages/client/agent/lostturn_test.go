package agent_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/session"
)

// The process stopped while a tool call ran: the transcript ends with the
// assistant's tool_use. Resuming must give the call a result (else the next
// request is refused) and end the lost turn as interrupted.
func TestResumeEndsTurnLostMidToolCall(t *testing.T) {
	e := setup(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m1", fg.ToolUse("t_read", "Read", map[string]any{"file_path": filepath.Join(fg.Cwd(r), "CLAUDE.md")}))}
	}))
	ctx := context.Background()
	s := e.open(t, nil)
	s.Agent.RunTurn(ctx, prompt("read it"))
	id, path := s.ID, s.Transcript.Path()
	s.Close()
	truncateAfterToolUse(t, path)

	r := e.open(t, func(o *bootstrap.Options) { o.Resume = id })
	msgs := r.Agent.Messages()
	checkPairing(t, msgs)
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || last.Content[0].Type != llm.BlockToolResult || !last.Content[0].IsError {
		t.Fatalf("the lost call must get an error result: %+v", last)
	}
	res := r.Agent.RunTurn(ctx, prompt("again"))
	if res.Subtype != agent.SubtypeSuccess {
		t.Fatalf("next turn: %+v", res)
	}
	reqs := e.srv.Requests()
	checkPairing(t, reqs[len(reqs)-1].Messages)
	r.Close()

	// The transcript now records the end of the lost turn: a resume needs no repair.
	entries, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	conv := session.Rebuild(entries)
	checkPairing(t, conv.Messages)
	if !strings.Contains(messagesText(conv.Messages), agent.InterruptedFallback) {
		t.Fatalf("interrupt marker not recorded: %+v", conv.Messages)
	}
}

// truncateAfterToolUse cuts the transcript right after the first assistant
// entry with a tool_use.
func truncateAfterToolUse(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		keep = append(keep, line)
		var e session.Entry
		if json.Unmarshal([]byte(line), &e) == nil && e.Type == session.TypeAssistant {
			if len((llm.Message{Content: e.Blocks()}).ToolUses()) > 0 {
				break
			}
		}
	}
	f.Close()
	if err := os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func messagesText(msgs []llm.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		for _, b := range m.Content {
			sb.WriteString(b.Text)
			for _, c := range b.Content {
				sb.WriteString(c.Text)
			}
		}
	}
	return sb.String()
}
