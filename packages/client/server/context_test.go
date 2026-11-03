package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/server"
)

// compactingResponder answers compaction requests with a summary and other requests with "ok", reporting
// inputTokens as the context size.
func compactingResponder(inputTokens int) fg.Responder {
	return func(n int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("s", fg.Text("SUMMARY"))}
		}
		return fg.Reply{Events: fg.WithInputTokens(fg.Message("m", fg.Text("ok")), inputTokens)}
	}
}

func TestGetContextUsage(t *testing.T) {
	h := startHarness(t, compactingResponder(50_000), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	before, err := h.client.GetContextUsage(h.ctx, connect.NewRequest(&agentv1.GetContextUsageRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if u := before.Msg.Usage; !u.Estimated || u.ContextWindow != 200_000 || u.UsedTokens <= 0 || u.AutocompactThreshold <= 0 {
		t.Fatalf("before the first turn %+v", u)
	}
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	res, err := h.client.GetContextUsage(h.ctx, connect.NewRequest(&agentv1.GetContextUsageRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	u := res.Msg.Usage
	// The provider counted 50 000 input + 20 output tokens; the categories add up to it and to the window.
	sum := int64(0)
	for _, c := range u.Categories {
		sum += c.Tokens
	}
	if u.Estimated || u.UsedTokens < 50_020 || u.UsedTokens > 50_100 || sum != u.ContextWindow || u.Categories[0].Category != agentv1.ContextUsage_CATEGORY_MESSAGES {
		t.Fatalf("after a turn: used=%d sum=%d %+v", u.UsedTokens, sum, u)
	}
	if u.TokensUntilAutocompact != u.AutocompactThreshold-u.UsedTokens {
		t.Fatalf("until auto-compact %d", u.TokensUntilAutocompact)
	}
	if _, err := h.client.GetContextUsage(h.ctx, connect.NewRequest(&agentv1.GetContextUsageRequest{SessionId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestCompactSlashCommand(t *testing.T) {
	h := startHarness(t, compactingResponder(1000), nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	h.send(sid, "/compact keep the greeting")
	var working bool
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if s := ev.GetSessionUpdated(); s != nil && s.Status == agentv1.SessionStatus_SESSION_STATUS_WORKING {
			working = true
		}
		if ev.GetUserMessage() != nil || ev.GetTurnStarted() != nil {
			t.Fatal("/compact started a turn")
		}
		c := ev.GetCompacted()
		return c != nil && c.Trigger == agentv1.Compacted_TRIGGER_MANUAL
	})
	if !working {
		t.Fatal("the session was not working while it compacted")
	}
	reqs := compactions(h)
	if len(reqs) != 1 || !strings.Contains(lastText(reqs[0]), "keep the greeting") {
		t.Fatalf("compaction requests %d", len(reqs))
	}
}

func TestIdleCompaction(t *testing.T) {
	var srv *server.Server
	h := startHarnessSrv(t, compactingResponder(150_000), nil, nil, func(s *server.Server) { srv = s })
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	now := time.Now()
	srv.CheckIdleAt(now.Add(server.IdleCompactAfter / 2))
	srv.CheckIdleAt(now.Add(server.IdleCompactAfter + time.Minute))
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		c := ev.GetCompacted()
		return c != nil && c.Trigger == agentv1.Compacted_TRIGGER_IDLE
	})
	n := len(compactions(h))
	// Handled: not compacted again until the next turn, however long it stays idle.
	srv.CheckIdleAt(now.Add(10 * server.IdleCompactAfter))
	time.Sleep(50 * time.Millisecond)
	if got := len(compactions(h)); got != n || n != 1 {
		t.Fatalf("%d compactions, want 1", got)
	}
}

func TestIdleCompactionTurnedOff(t *testing.T) {
	var srv *server.Server
	h := startHarnessSrv(t, compactingResponder(150_000), nil, nil, func(s *server.Server) { srv = s })
	// The user's settings turn it off (here the user-level settings file used without sync).
	writeUserSettings(t, `{"compactIdleSessions": false}`)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hello")
	h.until(ch, isTurnFinished)
	srv.CheckIdleAt(time.Now().Add(2 * server.IdleCompactAfter))
	time.Sleep(50 * time.Millisecond)
	if n := len(compactions(h)); n != 0 {
		t.Fatalf("compacted although turned off (%d requests)", n)
	}
}

func compactions(h *harness) []*llm.Request {
	var out []*llm.Request
	for _, r := range h.gw.Requests() {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			out = append(out, r)
		}
	}
	return out
}

// writeUserSettings writes the user-level settings file (used when settings are not synced).
func writeUserSettings(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lastText(r *llm.Request) string {
	m := r.Messages[len(r.Messages)-1]
	var b strings.Builder
	for _, c := range m.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}
