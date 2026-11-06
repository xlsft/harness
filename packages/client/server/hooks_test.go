package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// hookCfg is one handler in the settings format.
type hookCfg struct {
	event, matcher, command string
	timeout                 float64
}

// hookSettings renders handlers as a settings file.
func hookSettings(t *testing.T, extra map[string]any, cfgs ...hookCfg) []byte {
	t.Helper()
	events := map[string][]map[string]any{}
	for _, c := range cfgs {
		h := map[string]any{"type": "command", "command": c.command}
		if c.timeout > 0 {
			h["timeout"] = c.timeout
		}
		events[c.event] = append(events[c.event], map[string]any{"matcher": c.matcher, "hooks": []any{h}})
	}
	doc := map[string]any{"hooks": events}
	for k, v := range extra {
		doc[k] = v
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// startHooked starts a harness in a trusted folder whose .claude/settings.json holds settings. The hook tests get their own
// home, so nothing of the developer's machine takes part.
func startHooked(t *testing.T, respond fg.Responder, settingsFile []byte) *harness {
	t.Helper()
	return startHookedTrust(t, respond, settingsFile, true)
}

func startHookedTrust(t *testing.T, respond fg.Responder, settingsFile []byte, trusted bool) *harness {
	t.Helper()
	h := startHarness(t, respond, func(o *bootstrap.Options) { o.Trusted = &trusted })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARNESS_NO_HOOKS", "")
	writeProjectFile(t, h.proj, ".claude/settings.json", settingsFile)
	return h
}

func writeProjectFile(t *testing.T, dir, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// script writes an executable shell script and returns its path, quoted for a command line.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "'" + p + "'"
}

// hookRuns are the finished hook runs among events.
func hookRuns(evs []*agentv1.SessionEvent) []*agentv1.HookRun {
	var out []*agentv1.HookRun
	for _, ev := range evs {
		if r := ev.GetHookRun(); r != nil && r.Status != agentv1.HookRun_STATUS_RUNNING {
			out = append(out, r)
		}
	}
	return out
}

func hookRequestText(h *harness) string {
	var sb strings.Builder
	for _, r := range h.gw.Requests() {
		sb.WriteString(allText(r))
	}
	return sb.String()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPreToolUseExitTwoRefusesTheCall(t *testing.T) {
	var target string
	h := startHooked(t, fg.Sequence(
		func(r *llm.Request) fg.Reply {
			target = filepath.Join(fg.Cwd(r), "x.txt")
			return fg.Reply{Events: fg.Message("m", fg.ToolUse("w", "Write", map[string]any{"file_path": target, "content": "no"}))}
		},
		say("understood"),
	), hookSettings(t, nil, hookCfg{event: "PreToolUse", matcher: "Write|Edit", command: "echo 'no writes today' >&2; exit 2"},
		hookCfg{event: "PreToolUse", matcher: "Bash", command: "exit 1"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "write")
	evs := h.until(ch, isTurnFinished)
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the refused call ran")
	}
	if got := results(h)["w"]; !strings.Contains(got, "no writes today") {
		t.Fatalf("result: %q", got)
	}
	runs := hookRuns(evs)
	if len(runs) != 1 {
		t.Fatalf("the matcher should pick one handler: %+v", runs)
	}
	r := runs[0]
	if r.Event != "PreToolUse" || r.Status != agentv1.HookRun_STATUS_BLOCKED || r.Decision != "deny" || r.ToolName != "Write" || r.ToolUseId != "w" || r.GetExitCode() != 2 || r.Source != "project" {
		t.Fatalf("run: %+v", r)
	}
}

func TestPreToolUseAllowPassesAPromptButNotADenyRule(t *testing.T) {
	allow := `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"fine"}}'`
	for _, tc := range []struct {
		name, command string
		denied        bool
	}{
		{name: "prompt passed", command: "touch made.txt"},
		{name: "deny rule stands", command: "rm -f made.txt", denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": tc.command, "description": "x"}), say("done")),
				hookSettings(t, map[string]any{"permissions": map[string]any{"deny": []string{"Bash(rm *)"}}}, hookCfg{event: "PreToolUse", matcher: "Bash", command: allow}))
			sid, ch := h.create(&agentv1.CreateSessionRequest{})
			h.send(sid, "go")
			evs := h.until(ch, isTurnFinished)
			for _, ev := range evs {
				if ev.GetPermissionRequest() != nil {
					t.Fatal("the hook's allow should answer the prompt")
				}
			}
			got := results(h)["b"]
			if tc.denied != strings.Contains(got, "denied by rule") {
				t.Fatalf("result: %q", got)
			}
			if !tc.denied {
				if _, err := os.Stat(filepath.Join(h.proj, "made.txt")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPreToolUseAskPromptsWhatWouldRun(t *testing.T) {
	ask := `echo '{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"look at this one"}}'`
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "pwd", "description": "x"}), say("done")),
		hookSettings(t, nil, hookCfg{event: "PreToolUse", command: ask}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var req *agentv1.PermissionRequest
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		req = ev.GetPermissionRequest()
		return req != nil
	})
	if req.Reason != "look at this one" {
		t.Fatalf("reason: %q", req.Reason)
	}
	if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: req.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, Comment: "not that"})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, isTurnFinished)
	if got := results(h)["b"]; !strings.Contains(got, "not that") {
		t.Fatalf("result: %q", got)
	}
}

func TestPreToolUseRewritesInputAndTheRulesJudgeTheRewrite(t *testing.T) {
	rewrite := func(command string) string {
		return `echo '{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"` + command + `","description":"x"}}}'`
	}
	t.Run("the rewrite runs", func(t *testing.T) {
		h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch model.txt", "description": "x"}), say("done")),
			hookSettings(t, nil, hookCfg{event: "PreToolUse", matcher: "Bash", command: rewrite("touch hook.txt")}))
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "go")
		h.until(ch, isTurnFinished)
		if _, err := os.Stat(filepath.Join(h.proj, "hook.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(h.proj, "model.txt")); err == nil {
			t.Fatal("the model's input ran")
		}
	})
	t.Run("a deny rule catches the rewrite", func(t *testing.T) {
		h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "pwd", "description": "x"}), say("done")),
			hookSettings(t, map[string]any{"permissions": map[string]any{"deny": []string{"Bash(touch *)"}}}, hookCfg{event: "PreToolUse", matcher: "Bash", command: rewrite("touch hook.txt")}))
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "go")
		h.until(ch, isTurnFinished)
		if _, err := os.Stat(filepath.Join(h.proj, "hook.txt")); err == nil {
			t.Fatal("a rewrite got past a deny rule")
		}
	})
}

func TestPermissionRequestHook(t *testing.T) {
	allow := `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}'`
	deny := `echo '{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"policy says no"}}}'`
	rewrite := `echo '{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedInput":{"command":"touch denied.txt","description":"x"}}}}'`
	t.Run("allow answers the prompt", func(t *testing.T) {
		h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("done")),
			hookSettings(t, nil, hookCfg{event: "PermissionRequest", matcher: "Bash", command: allow}))
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "go")
		for _, ev := range h.until(ch, isTurnFinished) {
			if ev.GetPermissionRequest() != nil {
				t.Fatal("the user was asked")
			}
		}
		if _, err := os.Stat(filepath.Join(h.proj, "a.txt")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("deny answers the prompt", func(t *testing.T) {
		h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("done")),
			hookSettings(t, nil, hookCfg{event: "PermissionRequest", command: deny}))
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "go")
		h.until(ch, isTurnFinished)
		if got := results(h)["b"]; !strings.Contains(got, "policy says no") {
			t.Fatalf("result: %q", got)
		}
	})
	t.Run("a rewrite does not get past a deny rule", func(t *testing.T) {
		h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("done")),
			hookSettings(t, map[string]any{"permissions": map[string]any{"deny": []string{"Bash(touch denied*)"}}}, hookCfg{event: "PermissionRequest", command: rewrite}))
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "go")
		h.until(ch, isTurnFinished)
		if _, err := os.Stat(filepath.Join(h.proj, "denied.txt")); err == nil {
			t.Fatal("a rewrite got past a deny rule")
		}
	})
	t.Run("a prompt only the user may answer stays theirs", func(t *testing.T) {
		h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("done")),
			hookSettings(t, map[string]any{"permissions": map[string]any{"ask": []string{"Bash(touch *)"}}}, hookCfg{event: "PermissionRequest", command: allow}))
		sid, ch := h.create(&agentv1.CreateSessionRequest{})
		h.send(sid, "go")
		var req *agentv1.PermissionRequest
		h.until(ch, func(ev *agentv1.SessionEvent) bool {
			req = ev.GetPermissionRequest()
			return req != nil
		})
		if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: req.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, Comment: "no"})); err != nil {
			t.Fatal(err)
		}
		h.until(ch, isTurnFinished)
	})
}

func TestPostToolUseSeesTheCallAndTalksToTheModel(t *testing.T) {
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin.json")
	env := filepath.Join(dir, "env.txt")
	hook := script(t, dir, "post.sh", `cat > '`+stdin+`'
echo "$CLAUDE_PROJECT_DIR|$HARNESS_PROJECT_DIR|$PWD" > '`+env+`'
echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"lint found 2 problems"}}'`)
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}), say("done")),
		hookSettings(t, nil, hookCfg{event: "PostToolUse", matcher: "Bash", command: hook}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	var in struct {
		SessionID  string          `json:"session_id"`
		Event      string          `json:"hook_event_name"`
		Tool       string          `json:"tool_name"`
		UseID      string          `json:"tool_use_id"`
		Input      json.RawMessage `json:"tool_input"`
		Response   json.RawMessage `json:"tool_response"`
		Transcript string          `json:"transcript_path"`
		Mode       string          `json:"permission_mode"`
	}
	if err := json.Unmarshal([]byte(readFile(t, stdin)), &in); err != nil {
		t.Fatal(err)
	}
	if in.SessionID != sid || in.Event != "PostToolUse" || in.Tool != "Bash" || in.UseID != "b" || !strings.Contains(string(in.Input), "echo hi") || !strings.Contains(string(in.Response), "hi") || in.Transcript == "" || in.Mode != "bypassPermissions" {
		t.Fatalf("stdin: %s", readFile(t, stdin))
	}
	if got := strings.TrimSpace(readFile(t, env)); got != h.proj+"|"+h.proj+"|"+h.proj {
		t.Fatalf("environment: %q", got)
	}
	if got := hookRequestText(h); !strings.Contains(got, "lint found 2 problems") {
		t.Fatal("the model did not get the context")
	}
	if runs := hookRuns(evs); len(runs) != 1 || runs[0].Status != agentv1.HookRun_STATUS_OK || !strings.Contains(runs[0].Output, "lint found") {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestPostToolUseFailureAndDecisionBlock(t *testing.T) {
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin.json")
	h := startHooked(t, fg.Sequence(
		call("r", "Read", map[string]any{"file_path": filepath.Join(dir, "absent.txt")}),
		call("w", "Bash", map[string]any{"command": "echo ok", "description": "x"}),
		say("done"),
	), hookSettings(t, nil,
		hookCfg{event: "PostToolUseFailure", matcher: "Read", command: "cat > '" + stdin + "'; echo 'try another path' >&2; exit 2"},
		hookCfg{event: "PostToolUse", matcher: "Bash", command: `echo '{"decision":"block","reason":"the format check failed"}'`}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if got := readFile(t, stdin); !strings.Contains(got, `"error"`) || !strings.Contains(got, `"tool_name":"Read"`) {
		t.Fatalf("stdin: %s", got)
	}
	got := hookRequestText(h)
	if !strings.Contains(got, "try another path") || !strings.Contains(got, "the format check failed") {
		t.Fatalf("the model did not hear: %s", got)
	}
}

func TestUserPromptSubmitBlocksAndAddsContext(t *testing.T) {
	blockIfSecret := `input=$(cat)
case "$input" in *secret*) echo 'that prompt has a secret' >&2; exit 2;; esac
echo 'the branch is frozen'`
	dir := t.TempDir()
	h := startHooked(t, func(_ int, r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("answer"))} },
		hookSettings(t, nil, hookCfg{event: "UserPromptSubmit", matcher: "ignored", command: script(t, dir, "p.sh", blockIfSecret)}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "the secret is 42")
	evs := h.until(ch, isTurnFinished)
	if n := len(h.gw.Requests()); n != 0 {
		t.Fatalf("a refused prompt reached the model: %d requests", n)
	}
	var said, shown bool
	for _, ev := range evs {
		if n := ev.GetNotice(); n != nil && strings.Contains(n.Text, "that prompt has a secret") {
			said = true
		}
		if u := ev.GetUserMessage(); u != nil {
			shown = true
		}
	}
	if !said || !shown {
		t.Fatalf("frontends must hear of it: notice %v, message %v", said, shown)
	}
	h.send(sid, "a fair question")
	h.until(ch, isTurnFinished)
	got := hookRequestText(h)
	if !strings.Contains(got, "the branch is frozen") || !strings.Contains(got, "a fair question") {
		t.Fatalf("request: %s", got)
	}
	if strings.Contains(got, "42") {
		t.Fatal("the refused prompt is in the conversation")
	}
}

func TestStopHookMakesTheAgentContinue(t *testing.T) {
	dir := t.TempDir()
	hook := script(t, dir, "stop.sh", `input=$(cat)
case "$input" in
  *'"stop_hook_active":true'*) exit 0;;
esac
echo '{"decision":"block","reason":"the tests have not run"}'`)
	h := startHooked(t, fg.Sequence(say("all done"), say("now really done")), hookSettings(t, nil, hookCfg{event: "Stop", command: hook}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if n := len(h.gw.Requests()); n != 2 {
		t.Fatalf("requests: %d", n)
	}
	if got := allText(h.gw.Requests()[1]); !strings.Contains(got, "the tests have not run") {
		t.Fatalf("second request: %s", got)
	}
	runs := hookRuns(evs)
	if len(runs) != 2 || runs[0].Status != agentv1.HookRun_STATUS_BLOCKED || runs[0].Decision != "block" || runs[1].Status != agentv1.HookRun_STATUS_OK {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestStopHookThatNeverLetsGoIsCut(t *testing.T) {
	h := startHooked(t, func(_ int, r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("done"))} },
		hookSettings(t, nil, hookCfg{event: "Stop", command: "echo 'not yet' >&2; exit 2"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if n := len(h.gw.Requests()); n > 20 {
		t.Fatalf("requests: %d", n)
	}
	cut := false
	for _, ev := range evs {
		if n := ev.GetNotice(); n != nil && strings.Contains(n.Text, "too many times") {
			cut = true
		}
	}
	if !cut {
		t.Fatal("no notice said why the turn ended")
	}
}

func TestContinueFalseEndsTheTurn(t *testing.T) {
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("never")),
		hookSettings(t, nil, hookCfg{event: "PostToolUse", command: `echo '{"continue":false,"stopReason":"budget used up"}'`}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if n := len(h.gw.Requests()); n != 1 {
		t.Fatalf("requests: %d", n)
	}
	seen := false
	for _, ev := range evs {
		if n := ev.GetNotice(); n != nil && n.Text == "budget used up" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the reason did not reach the user")
	}
}

func TestSessionStartAndEnd(t *testing.T) {
	dir := t.TempDir()
	end := filepath.Join(dir, "end.json")
	h := startHooked(t, fg.Sequence(say("hello")), hookSettings(t, nil,
		hookCfg{event: "SessionStart", matcher: "startup", command: "cat >/dev/null; echo 'we are on the release branch'"},
		hookCfg{event: "SessionStart", matcher: "resume", command: "echo 'wrong source'"},
		hookCfg{event: "SessionEnd", command: "cat > '" + end + "'"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	got := hookRequestText(h)
	if !strings.Contains(got, "we are on the release branch") || strings.Contains(got, "wrong source") {
		t.Fatalf("request: %s", got)
	}
	if runs := hookRuns(evs); len(runs) != 1 || runs[0].Event != "SessionStart" {
		t.Fatalf("runs: %+v", runs)
	}
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, end); !strings.Contains(got, `"hook_event_name":"SessionEnd"`) || !strings.Contains(got, `"reason":"`) {
		t.Fatalf("stdin: %s", got)
	}
}

func TestCompactionHooks(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "refuse")
	post := filepath.Join(dir, "post.json")
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := startHooked(t, func(_ int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" {
			return fg.Reply{Events: fg.Message("c", fg.Text("the summary"))}
		}
		return fg.Reply{Events: fg.Message("m", fg.Text("ok"))}
	}, hookSettings(t, nil,
		hookCfg{event: "PreCompact", matcher: "manual", command: "if [ -e '" + flag + "' ]; then echo 'not during a release' >&2; exit 2; fi; echo 'keep the numbers'"},
		hookCfg{event: "PostCompact", command: "cat > '" + post + "'"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if _, err := h.client.Compact(h.ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: sid})); err == nil || !strings.Contains(err.Error(), "not during a release") {
		t.Fatalf("refused compaction: %v", err)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if _, err := h.client.Compact(h.ctx, connect.NewRequest(&agentv1.CompactRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, func(ev *agentv1.SessionEvent) bool { return ev.GetCompacted() != nil })
	if got := readFile(t, post); !strings.Contains(got, `"compact_summary":"the summary"`) || !strings.Contains(got, `"trigger":"manual"`) {
		t.Fatalf("stdin: %s", got)
	}
	var instructed bool
	for _, r := range h.gw.Requests() {
		if r.Metadata != nil && r.Metadata.RequestClass == "compaction" && strings.Contains(allText(r), "keep the numbers") {
			instructed = true
		}
	}
	if !instructed {
		t.Fatal("what PreCompact said did not reach the compaction")
	}
}

func TestNotificationOnAPrompt(t *testing.T) {
	dir := t.TempDir()
	note := filepath.Join(dir, "note.json")
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("done")),
		hookSettings(t, nil, hookCfg{event: "Notification", matcher: "permission_prompt", command: "cat > '" + note + "'"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	var req *agentv1.PermissionRequest
	var run *agentv1.HookRun
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if p := ev.GetPermissionRequest(); p != nil {
			req = p
		}
		if r := ev.GetHookRun(); r != nil && r.Status != agentv1.HookRun_STATUS_RUNNING {
			run = r
		}
		return req != nil && run != nil
	})
	if got := readFile(t, note); !strings.Contains(got, `"notification_type":"permission_prompt"`) || !strings.Contains(got, "Bash") {
		t.Fatalf("stdin: %s", got)
	}
	if _, err := h.client.RespondToPermission(h.ctx, connect.NewRequest(&agentv1.RespondToPermissionRequest{SessionId: sid, RequestId: req.RequestId, Decision: agentv1.RespondToPermissionRequest_DECISION_DENY, Comment: "no"})); err != nil {
		t.Fatal(err)
	}
	h.until(ch, isTurnFinished)
}

func TestSubagentStartAndStop(t *testing.T) {
	dir := t.TempDir()
	start := filepath.Join(dir, "start.json")
	stop := filepath.Join(dir, "stop.json")
	stopHook := script(t, dir, "stop.sh", `input=$(cat)
echo "$input" > '`+stop+`'
case "$input" in
  *'"stop_hook_active":true'*) exit 0;;
esac
echo 'check your work' >&2
exit 2`)
	var subTexts []string
	h := startHooked(t, func(_ int, r *llm.Request) fg.Reply {
		if r.Metadata != nil && r.Metadata.RequestClass == "subagent" {
			subTexts = append(subTexts, allText(r))
			return fg.Reply{Events: fg.Message("s", fg.Text("agent report"))}
		}
		switch {
		case strings.Contains(allText(r), "agent report"):
			return fg.Reply{Events: fg.Message("z", fg.Text("all done"))}
		case len(fg.LastToolResults(r)) > 0:
			return fg.Reply{Events: fg.Message("y", fg.Text("waiting"))}
		}
		return fg.Reply{Events: fg.Message("a", fg.ToolUse("a1", "Agent", map[string]any{"description": "look", "prompt": "look around", "subagent_type": "general-purpose"}))}
	}, hookSettings(t, nil,
		hookCfg{event: "SubagentStart", matcher: "general-purpose", command: "cat > '" + start + "'; echo '{\"hookSpecificOutput\":{\"additionalContext\":\"agents write in English\"}}'"},
		hookCfg{event: "SubagentStop", command: stopHook}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	var runs []*agentv1.HookRun
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetHookRun(); r != nil && r.Status != agentv1.HookRun_STATUS_RUNNING {
			runs = append(runs, r)
		}
		u := ev.GetSubagentUpdated()
		return u != nil && u.Agent.State == agentv1.SubagentState_SUBAGENT_STATE_FINISHED
	})
	if got := readFile(t, start); !strings.Contains(got, `"agent_type":"general-purpose"`) || !strings.Contains(got, `"agent_id":"`) {
		t.Fatalf("start stdin: %s", got)
	}
	if got := readFile(t, stop); !strings.Contains(got, `"agent_transcript_path":"`) || !strings.Contains(got, `"last_assistant_message":"agent report"`) {
		t.Fatalf("stop stdin: %s", got)
	}
	if len(subTexts) != 2 || !strings.Contains(subTexts[0], "agents write in English") || !strings.Contains(subTexts[1], "check your work") {
		t.Fatalf("agent requests: %q", subTexts)
	}
	if len(runs) != 3 || runs[0].AgentId == "" {
		t.Fatalf("the session shows the agent's hooks: %+v", runs)
	}
}

func TestHookTimeoutIsReportedAndTheTurnGoesOn(t *testing.T) {
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}), say("done")),
		hookSettings(t, nil, hookCfg{event: "PreToolUse", command: "sleep 60", timeout: 0.3}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	runs := hookRuns(evs)
	if len(runs) != 1 || runs[0].Status != agentv1.HookRun_STATUS_TIMED_OUT {
		t.Fatalf("runs: %+v", runs)
	}
	if got := results(h)["b"]; !strings.Contains(got, "hi") {
		t.Fatalf("a slow hook is no refusal: %q", got)
	}
}

func TestInterruptKillsARunningHook(t *testing.T) {
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}), say("done")),
		hookSettings(t, nil, hookCfg{event: "PreToolUse", command: "sleep 60"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		r := ev.GetHookRun()
		return r != nil && r.Status == agentv1.HookRun_STATUS_RUNNING
	})
	if _, err := h.client.Interrupt(h.ctx, connect.NewRequest(&agentv1.InterruptRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	evs := h.until(ch, isTurnFinished)
	runs := hookRuns(evs)
	if len(runs) != 1 || runs[0].Status != agentv1.HookRun_STATUS_CANCELED {
		t.Fatalf("runs: %+v", runs)
	}
}

// Each handler waits for a file the other writes: they only end if they run at the same time. The same command under a second
// matcher group runs once.
func TestHooksRunInParallelAndDedupe(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	count := filepath.Join(dir, "count")
	wait := func(mine, theirs string) string {
		return fmt.Sprintf("touch '%s'; i=0; while [ ! -e '%s' ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done; [ -e '%s' ]", mine, theirs, theirs)
	}
	once := "echo x >> '" + count + "'"
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}), say("done")),
		hookSettings(t, nil, hookCfg{event: "PreToolUse", command: wait(a, b)}, hookCfg{event: "PreToolUse", command: wait(b, a)},
			hookCfg{event: "PreToolUse", matcher: "Bash", command: once}, hookCfg{event: "PreToolUse", matcher: "*", command: once}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	runs := hookRuns(evs)
	if len(runs) != 3 {
		t.Fatalf("runs: %+v", runs)
	}
	for _, r := range runs {
		if r.Status != agentv1.HookRun_STATUS_OK {
			t.Fatalf("run: %+v", r)
		}
	}
	if got := readFile(t, count); got != "x\n" {
		t.Fatalf("the same command ran %d times", strings.Count(got, "x"))
	}
}

func TestHTTPHandler(t *testing.T) {
	var body []byte
	var auth, other string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		auth, other = r.Header.Get("Authorization"), r.Header.Get("X-Other")
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"the service said no"}}`))
	}))
	defer srv.Close()
	t.Setenv("HOOK_TOKEN", "t0ken")
	t.Setenv("OTHER_SECRET", "s3cret")
	doc, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{
		"type": "http", "url": srv.URL, "allowedEnvVars": []string{"HOOK_TOKEN"},
		"headers": map[string]string{"Authorization": "Bearer $HOOK_TOKEN", "X-Other": "${OTHER_SECRET}"},
	}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "touch a.txt", "description": "x"}), say("done")), doc)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if !strings.Contains(string(body), `"tool_name":"Bash"`) {
		t.Fatalf("body: %s", body)
	}
	if auth != "Bearer t0ken" || other != "" {
		t.Fatalf("headers: %q %q", auth, other)
	}
	if got := results(h)["b"]; !strings.Contains(got, "the service said no") {
		t.Fatalf("result: %q", got)
	}
	if runs := hookRuns(evs); len(runs) != 1 || runs[0].HandlerType != "http" || runs[0].GetExitCode() != 200 {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestUntrustedFolderRunsNoProjectHooks(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	doc := hookSettings(t, nil, hookCfg{event: "PreToolUse", command: "touch '" + mark + "'"}, hookCfg{event: "UserPromptSubmit", command: "touch '" + mark + "'"})
	h := startHookedTrust(t, fg.Sequence(say("hello")), doc, false)
	writeProjectFile(t, h.proj, ".harness/settings.local.json", doc)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("a hook of an untrusted folder ran")
	}
	if runs := hookRuns(evs); len(runs) != 0 {
		t.Fatalf("runs: %+v", runs)
	}
	list, err := h.client.ListHooks(h.ctx, connect.NewRequest(&agentv1.ListHooksRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Hooks) != 4 {
		t.Fatalf("listed: %+v", list.Msg.Hooks)
	}
	for _, c := range list.Msg.Hooks {
		if c.Status != agentv1.ConfiguredHook_STATUS_NEEDS_WORKSPACE_TRUST {
			t.Fatalf("listed: %+v", c)
		}
	}
}

func TestListHooksAndSwitchInARunningSession(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "count")
	doc, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo x >> '" + mark + "'", "timeout": 5}}}},
		"PreToolUse":       []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "prompt", "prompt": "is it safe?"}}}},
		"TaskCompleted":    []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "true"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := startHooked(t, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }, doc)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})

	list, err := h.client.ListHooks(h.ctx, connect.NewRequest(&agentv1.ListHooksRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	byEvent := map[string]*agentv1.ConfiguredHook{}
	for _, c := range list.Msg.Hooks {
		byEvent[c.Event] = c
	}
	active := byEvent["UserPromptSubmit"]
	if !list.Msg.Snapshot || len(list.Msg.Hooks) != 3 || active.GetStatus() != agentv1.ConfiguredHook_STATUS_ACTIVE || active.TimeoutSeconds != 5 || active.Source != "project" || !strings.HasSuffix(active.SourcePath, "settings.json") {
		t.Fatalf("listed: %+v", list.Msg)
	}
	if c := byEvent["PreToolUse"]; c.Status != agentv1.ConfiguredHook_STATUS_UNSUPPORTED || !strings.Contains(c.Reason, "prompt") {
		t.Fatalf("a handler type this client skips: %+v", c)
	}
	if c := byEvent["TaskCompleted"]; c.Status != agentv1.ConfiguredHook_STATUS_UNSUPPORTED || !strings.Contains(c.Reason, "TaskCompleted") {
		t.Fatalf("an event this client does not fire: %+v", c)
	}
	byFolder, err := h.client.ListHooks(h.ctx, connect.NewRequest(&agentv1.ListHooksRequest{Cwd: h.proj}))
	if err != nil || byFolder.Msg.Snapshot || len(byFolder.Msg.Hooks) != 3 || byFolder.Msg.Cwd != h.proj {
		t.Fatalf("by folder: %v %+v", err, byFolder)
	}

	h.send(sid, "one")
	h.until(ch, isTurnFinished)
	if got := readFile(t, mark); strings.Count(got, "x") != 1 {
		t.Fatalf("runs: %q", got)
	}
	// The switch reaches the running session, and the list says so.
	writeProjectFile(t, h.proj, ".harness/settings.local.json", []byte(`{"disableAllHooks":true}`))
	h.send(sid, "two")
	h.until(ch, isTurnFinished)
	if got := readFile(t, mark); strings.Count(got, "x") != 1 {
		t.Fatalf("a hook ran after the switch: %q", got)
	}
	list, err = h.client.ListHooks(h.ctx, connect.NewRequest(&agentv1.ListHooksRequest{SessionId: sid}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Msg.Hooks {
		if c.Event == "UserPromptSubmit" && c.Status != agentv1.ConfiguredHook_STATUS_DISABLED {
			t.Fatalf("after the switch: %+v", c)
		}
	}
}

// The set is what the session opened with: a settings file written mid-session, which is what a model could try, gets no hook
// run until the next session.
func TestHooksAreASnapshot(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "ran")
	h := startHooked(t, func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }, []byte(`{}`))
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	writeProjectFile(t, h.proj, ".claude/settings.json", hookSettings(t, nil, hookCfg{event: "UserPromptSubmit", command: "touch '" + mark + "'"}))
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("a hook written mid-session ran")
	}
}

func TestHookRunsSurviveResume(t *testing.T) {
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}), say("done"), say("again")),
		hookSettings(t, nil, hookCfg{event: "PostToolUse", command: "echo formatted"}))
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	h.until(ch, isTurnFinished)
	if _, err := h.client.CloseSession(h.ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	resumed, ch2 := h.create(&agentv1.CreateSessionRequest{ResumeSessionId: sid})
	if resumed != sid {
		t.Fatalf("resumed as %s", resumed)
	}
	h.send(resumed, "more")
	evs := h.until(ch2, func(ev *agentv1.SessionEvent) bool { return isTurnFinished(ev) && len(h.gw.Requests()) >= 3 })
	var replayed *agentv1.HookRun
	var ranTool bool
	for _, ev := range evs {
		if r := ev.GetHookRun(); r != nil && r.Event == "PostToolUse" {
			replayed = r
		}
		if ev.GetToolCallFinished().GetToolUseId() == "b" {
			ranTool = true
		}
	}
	if replayed == nil || replayed.Output != "formatted" || replayed.Status != agentv1.HookRun_STATUS_OK || !ranTool {
		t.Fatalf("replay: %+v, tool %v", replayed, ranTool)
	}
	// The conversation is whole: the model got the tool result back.
	reqs := h.gw.Requests()
	if got := allText(reqs[len(reqs)-1]); !strings.Contains(got, "more") {
		t.Fatalf("request: %s", got)
	}
}

func TestAsyncHookWaitsForNobody(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	doc, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{
		"type": "command", "async": true, "command": "i=0; while [ ! -e '" + release + "' ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done; echo finished",
	}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := startHooked(t, fg.Sequence(call("b", "Bash", map[string]any{"command": "echo hi", "description": "x"}), say("done")), doc)
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_BYPASS_PERMISSIONS})
	h.send(sid, "go")
	evs := h.until(ch, isTurnFinished)
	if len(hookRuns(evs)) != 0 {
		t.Fatal("the turn waited for the hook")
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var run *agentv1.HookRun
	h.until(ch, func(ev *agentv1.SessionEvent) bool {
		if r := ev.GetHookRun(); r != nil && r.Status != agentv1.HookRun_STATUS_RUNNING {
			run = r
			return true
		}
		return false
	})
	if !run.Async || run.Output != "finished" {
		t.Fatalf("run: %+v", run)
	}
}
