package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/prompts"
)

func TestOutputStyleRPCs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := startHarness(t, nil, nil)
	var sys []prompts.Section
	for _, s := range h.gw.Bundle.System {
		if s.ID == "tool_policy" {
			sys = append(sys, prompts.Section{ID: "doing_tasks", Text: "DOING TASKS SECTION"})
		}
		sys = append(sys, s)
	}
	h.gw.Bundle.System = sys
	dir := filepath.Join(h.proj, ".claude", "output-styles")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "terse.md"), []byte("---\nname: Terse\ndescription: One-line answers\n---\nAnswer in one line."), 0o644)

	sid, events := h.create(&agentv1.CreateSessionRequest{})
	list, err := h.client.ListOutputStyles(h.ctx, connect.NewRequest(&agentv1.ListOutputStylesRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, st := range list.Msg.GetStyles() {
		got = append(got, st.GetId()+":"+st.GetSource())
	}
	if strings.Join(got, ",") != "default:builtin,explanatory:builtin,learning:builtin,Terse:project" || list.Msg.GetActive() != "default" {
		t.Fatalf("styles %v active %q", got, list.Msg.GetActive())
	}
	if list.Msg.GetStyles()[3].GetDescription() != "One-line answers" || list.Msg.GetStyles()[1].GetName() != "Explanatory" {
		t.Fatalf("style info %+v", list.Msg.GetStyles())
	}

	if _, err := h.client.SetOutputStyle(h.ctx, connect.NewRequest(&agentv1.SetOutputStyleRequest{SessionId: sid, Style: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown style: %v", err)
	}
	// Before the first message the session is still at its start: the coding sections go now.
	if _, err := h.client.SetOutputStyle(h.ctx, connect.NewRequest(&agentv1.SetOutputStyleRequest{SessionId: sid, Style: "terse"})); err != nil {
		t.Fatal(err)
	}
	h.until(events, func(ev *agentv1.SessionEvent) bool {
		return ev.GetSessionUpdated().GetOutputStyle() == "Terse"
	})
	h.send(sid, "hello")
	h.until(events, isTurnFinished)
	r := h.gw.Requests()[0]
	var sb strings.Builder
	for _, b := range r.System {
		sb.WriteString(b.Text)
	}
	if strings.Contains(sb.String(), "DOING TASKS SECTION") || !strings.Contains(allText(r), "Output style: Terse\nAnswer in one line.") {
		t.Fatalf("system %q messages %q", sb.String(), allText(r))
	}
	list, _ = h.client.ListOutputStyles(h.ctx, connect.NewRequest(&agentv1.ListOutputStylesRequest{SessionId: sid}))
	if list.Msg.GetActive() != "Terse" {
		t.Fatalf("active %q", list.Msg.GetActive())
	}

	// Mid-session: switching back to a style with coding instructions is deferred (notice).
	if _, err := h.client.SetOutputStyle(h.ctx, connect.NewRequest(&agentv1.SetOutputStyleRequest{SessionId: sid, Style: "explanatory"})); err != nil {
		t.Fatal(err)
	}
	h.until(events, func(ev *agentv1.SessionEvent) bool { return ev.GetNotice().GetKind() == "output_style_deferred" })
	h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid}))

	// Resume reports the recorded style in SessionInfo.
	cs, err := h.client.CreateSession(h.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h.proj, ResumeSessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Msg.GetSession().GetOutputStyle() != "explanatory" {
		t.Fatalf("resumed output_style %q", cs.Msg.GetSession().GetOutputStyle())
	}
}

func TestOutputStyleBaseOptionAndUnknownWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := startHarness(t, nil, func(o *bootstrap.Options) { o.OutputStyle = "learning" })
	sid, _ := h.create(&agentv1.CreateSessionRequest{})
	list, err := h.client.ListOutputStyles(h.ctx, connect.NewRequest(&agentv1.ListOutputStylesRequest{SessionId: sid}))
	if err != nil || list.Msg.GetActive() != "learning" {
		t.Fatalf("--output-style on serve: %v %q", err, list.Msg.GetActive())
	}

	h2 := startHarness(t, nil, func(o *bootstrap.Options) { o.OutputStyle = "missing" })
	cs, err := h2.client.CreateSession(h2.ctx, connect.NewRequest(&agentv1.CreateSessionRequest{Cwd: h2.proj}))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Msg.GetSession().GetOutputStyle() != "default" {
		t.Fatalf("unknown style falls back to default: %q", cs.Msg.GetSession().GetOutputStyle())
	}
	events := h2.subscribe(cs.Msg.GetSession().GetId())
	h2.until(events, func(ev *agentv1.SessionEvent) bool {
		return ev.GetNotice().GetKind() == "session_warning" && strings.Contains(ev.GetNotice().GetText(), "missing")
	})
}
