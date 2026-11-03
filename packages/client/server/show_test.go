package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

func TestShowPublishesShowRequested(t *testing.T) {
	h := startHarness(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("s", "Show", map[string]any{"target": "file", "path": "src/x.go", "line": 2, "end_line": 3}))}
	}), nil)
	if err := os.MkdirAll(filepath.Join(h.proj, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.proj, "src", "x.go"), []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "show me")
	var show *agentv1.ShowRequested
	var started *agentv1.ToolCallStarted
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := ev.GetShowRequested(); s != nil {
			show = s
		}
		if s := ev.GetToolCallStarted(); s != nil {
			started = s
		}
		return isTurnFinished(ev)
	})
	want := filepath.Join(h.proj, "src", "x.go")
	if show == nil || show.ToolUseId != "s" || show.Kind != agentv1.ShowRequested_KIND_FILE || show.Path != want || show.LineStart != 2 || show.LineEnd != 3 {
		t.Fatalf("show %+v", show)
	}
	if started == nil || started.Summary != "Show src/x.go" {
		t.Fatalf("summary %+v", started)
	}
	if tr := lastResultText(h.gw.Requests()[1]); !strings.Contains(tr, "Opened src/x.go:2-3 in the Files pane.") {
		t.Fatalf("result %q", tr)
	}
}

func TestShowWithoutPanes(t *testing.T) {
	h := startHarnessSrv(t, fg.Sequence(func(r *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.ToolUse("s", "Show", map[string]any{"target": "url", "url": "http://localhost:3000"}))}
	}), nil, nil, func(s *server.Server) { s.NoPanes = true })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "show me")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if ev.GetShowRequested() != nil {
			t.Fatal("ShowRequested published without panes")
		}
		return isTurnFinished(ev)
	})
	if tr := lastResultText(h.gw.Requests()[1]); !strings.HasPrefix(tr, "Nothing was shown") {
		t.Fatalf("result %q", tr)
	}
}
