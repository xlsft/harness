package relay

import (
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
)

func TestScrubDeviceLocal(t *testing.T) {
	for _, ev := range []*agentv1.SessionEvent{
		{Event: &agentv1.SessionEvent_ComputerAccessRequested{ComputerAccessRequested: &agentv1.ComputerAccessRequested{RequestId: "r"}}},
		{Event: &agentv1.SessionEvent_ComputerAccessResolved{ComputerAccessResolved: &agentv1.ComputerAccessResolved{RequestId: "r"}}},
		{Event: &agentv1.SessionEvent_ComputerUseState{ComputerUseState: &agentv1.ComputerUseState{Controlling: true}}},
	} {
		ev.Seq = 7
		scrubDeviceLocal(ev)
		if ev.GetEvent() != nil || ev.GetSeq() != 7 {
			t.Fatalf("%+v", ev)
		}
	}
	image := &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Image{Image: &llmv1.ImageBlock{Data: []byte("pixels")}}}
	text := &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "Screenshot"}}}
	finished := func(structured string) *agentv1.SessionEvent {
		return &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{
			StructuredResultJson: structured, Content: []*llmv1.ContentBlock{text, image},
		}}}
	}
	ev := finished(`{"computer_use":true,"width":10}`)
	scrubDeviceLocal(ev)
	if c := ev.GetToolCallFinished().GetContent(); len(c) != 1 || c[0].GetText() == nil {
		t.Fatalf("%+v", c)
	}
	ev = finished(`{"tab":"t1"}`)
	scrubDeviceLocal(ev)
	if c := ev.GetToolCallFinished().GetContent(); len(c) != 2 {
		t.Fatalf("a browser screenshot changed: %+v", c)
	}
}
