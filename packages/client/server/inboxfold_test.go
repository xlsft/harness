package server

import (
	"strings"
	"testing"
	"time"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
)

// The same scenarios as packages/core/test/inbox.test.ts ("fold").
func TestInboxFoldGroups(t *testing.T) {
	f := newInboxFold("s")
	now := time.Now()
	fold := func(ev *agentv1.SessionEvent) { f.fold(ev, now) }
	want := func(g agentv1.InboxGroup) {
		t.Helper()
		if got := f.group(); got != g {
			t.Fatalf("group %v, want %v (%+v)", got, g, f.item)
		}
	}
	want(agentv1.InboxGroup_INBOX_GROUP_UNSPECIFIED)
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnStarted{TurnStarted: &agentv1.TurnStarted{}}})
	want(agentv1.InboxGroup_INBOX_GROUP_RUNNING)
	fold(&agentv1.SessionEvent{Timestamp: "t2", Event: &agentv1.SessionEvent_PermissionRequest{PermissionRequest: &agentv1.PermissionRequest{RequestId: "p1", ToolName: "Bash", InputJson: `{"command":"rm -rf build"}`}}})
	want(agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT)
	if f.item.Requests[0].GetSummary() != "rm -rf build" || f.item.GetAttentionAt() != "t2" {
		t.Fatalf("request %+v", f.item)
	}
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_PermissionResolved{PermissionResolved: &agentv1.PermissionResolved{RequestId: "p1"}}})
	want(agentv1.InboxGroup_INBOX_GROUP_RUNNING)
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{Result: agentv1.TurnFinished_RESULT_ERROR_DURING_EXECUTION, ErrorMessage: "boom"}}})
	want(agentv1.InboxGroup_INBOX_GROUP_FAILED)
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnStarted{TurnStarted: &agentv1.TurnStarted{}}})
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Content: []*llmv1.ContentBlock{{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "All done."}}}}}}})
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_TurnFinished{TurnFinished: &agentv1.TurnFinished{Result: agentv1.TurnFinished_RESULT_SUCCESS, Usage: &llmv1.Usage{InputTokens: 100, OutputTokens: 20}}}})
	want(agentv1.InboxGroup_INBOX_GROUP_FINISHED)
	if f.item.GetReport() != "All done." || f.item.GetInputTokens() != 100 {
		t.Fatalf("finished %+v", f.item)
	}
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SubagentUpdated{SubagentUpdated: &agentv1.SubagentUpdated{Agent: &agentv1.SubagentInfo{Id: "a1", Name: "explore", State: agentv1.SubagentState_SUBAGENT_STATE_RUNNING}}}})
	want(agentv1.InboxGroup_INBOX_GROUP_RUNNING)
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SubagentUpdated{SubagentUpdated: &agentv1.SubagentUpdated{Agent: &agentv1.SubagentInfo{Id: "a1", Name: "explore", State: agentv1.SubagentState_SUBAGENT_STATE_FINISHED, Report: "found it"}}}})
	want(agentv1.InboxGroup_INBOX_GROUP_FINISHED)
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_Question{Question: &agentv1.Question{RequestId: "q1", Items: []*agentv1.QuestionItem{{Question: "Which one?", Header: "Pick", Options: []*agentv1.QuestionOption{{Label: "A"}, {Label: "B"}}}}}}})
	if q := f.item.Requests[0].GetQuestions()[0]; strings.Join(q.GetOptions(), ",") != "A,B" {
		t.Fatalf("question %+v", q)
	}
	fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionEnded{SessionEnded: &agentv1.SessionEnded{Reason: "exit"}}})
	if len(f.item.GetRequests()) != 0 || f.item.GetOpen() {
		t.Fatalf("ended %+v", f.item)
	}
}

func TestInboxFoldProjectRun(t *testing.T) {
	f := newInboxFold("c")
	f.fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ProjectRunUpdated{ProjectRunUpdated: &agentv1.ProjectRunUpdated{Run: &agentv1.ProjectRun{Id: "prun_1", State: agentv1.ProjectRunState_PROJECT_RUN_STATE_PAUSED, Reason: "Budget reached"}}}}, time.Now())
	if f.group() != agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT || f.item.Requests[0].GetKind() != agentv1.InboxRequest_KIND_PROJECT_RUN {
		t.Fatalf("paused %+v", f.item)
	}
	f.fold(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_ProjectRunUpdated{ProjectRunUpdated: &agentv1.ProjectRunUpdated{Run: &agentv1.ProjectRun{Id: "prun_1", State: agentv1.ProjectRunState_PROJECT_RUN_STATE_RUNNING}}}}, time.Now())
	if len(f.item.GetRequests()) != 0 {
		t.Fatalf("resumed %+v", f.item)
	}
}

func TestInputSummary(t *testing.T) {
	if s := inputSummary(`{"file_path":"/a/b.go","content":"x"}`); s != "/a/b.go" {
		t.Fatal(s)
	}
	if s := inputSummary("not json"); s != "not json" {
		t.Fatal(s)
	}
	if s := inputSummary(`{"command":"` + strings.Repeat("x", 1000) + `"}`); len([]rune(s)) > inboxSummaryChars {
		t.Fatal(len(s))
	}
}
