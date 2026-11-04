package relay

import (
	"errors"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
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
		scrubDeviceLocal(ev, nil, "")
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
	scrubDeviceLocal(ev, nil, "")
	if c := ev.GetToolCallFinished().GetContent(); len(c) != 1 || c[0].GetText() == nil {
		t.Fatalf("%+v", c)
	}
	ev = finished(`{"tab":"t1"}`)
	scrubDeviceLocal(ev, nil, "")
	if c := ev.GetToolCallFinished().GetContent(); len(c) != 2 {
		t.Fatalf("a browser screenshot changed: %+v", c)
	}
}

type sinkFake struct {
	available bool
	added     []string
	fail      bool
}

func (s *sinkFake) Available() bool { return s.available }

func (s *sinkFake) AddBytes(sessionID, kind, name, mediaType string, data []byte) (*commonv1.ObjectRef, error) {
	if s.fail {
		return nil, errors.New("disk full")
	}
	s.added = append(s.added, sessionID+"/"+kind+"/"+string(data))
	return &commonv1.ObjectRef{Sha256: "abc", Size: int64(len(data)), MediaType: mediaType, Name: name}, nil
}

func TestScrubReplacesImagesByReferences(t *testing.T) {
	image := func(b string) *llmv1.ContentBlock {
		return &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Image{Image: &llmv1.ImageBlock{MediaType: "image/png", Data: []byte(b)}}}
	}
	text := &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "typed"}}}
	tool := &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{
		StructuredResultJson: `{"computer_use":true}`, Content: []*llmv1.ContentBlock{text, image("shot")},
	}}}
	sink := &sinkFake{available: true}
	scrubDeviceLocal(tool, sink, "s1")
	c := tool.GetToolCallFinished().GetContent()
	if len(c) != 2 || c[0].GetText().GetText() != "typed" || c[1].GetImage() != nil || c[1].GetText().GetObject().GetSha256() != "abc" {
		t.Fatalf("%+v", c)
	}
	user := &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Content: []*llmv1.ContentBlock{text, image("photo")}}}}
	scrubDeviceLocal(user, sink, "s1")
	if u := user.GetUserMessage().GetContent(); u[1].GetText().GetObject() == nil {
		t.Fatalf("%+v", u)
	}
	if len(sink.added) != 2 || sink.added[0] != "s1/image/shot" || sink.added[1] != "s1/attachment/photo" {
		t.Fatalf("%v", sink.added)
	}

	// A store that is not there, or cannot take it: bytes do not travel for computer use, and nothing else changes.
	off := &agentv1.SessionEvent{Event: &agentv1.SessionEvent_UserMessage{UserMessage: &agentv1.UserMessage{Content: []*llmv1.ContentBlock{image("photo")}}}}
	scrubDeviceLocal(off, &sinkFake{}, "s1")
	if off.GetUserMessage().GetContent()[0].GetImage() == nil {
		t.Fatal("changed without a store")
	}
	failed := &agentv1.SessionEvent{Event: &agentv1.SessionEvent_ToolCallFinished{ToolCallFinished: &agentv1.ToolCallFinished{Content: []*llmv1.ContentBlock{image("shot")}}}}
	scrubDeviceLocal(failed, &sinkFake{available: true, fail: true}, "s1")
	if b := failed.GetToolCallFinished().GetContent()[0]; b.GetImage() != nil || b.GetText().GetText() == "" {
		t.Fatalf("%+v", b)
	}
}
