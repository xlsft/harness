package server_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// What the composer sends for an attached image and a PDF (lib/session/attachments.ts): the blocks reach the model as
// they are, and the transcript gives them back for the user's message.
func TestAttachmentsReachTheModel(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	pdf := []byte("%PDF-1.7")
	h := startHarness(t, func(_ int, _ *llm.Request) fg.Reply {
		return fg.Reply{Events: fg.Message("m", fg.Text("seen"))}
	}, nil)
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	content := []*llmv1.ContentBlock{
		{Block: &llmv1.ContentBlock_Image{Image: &llmv1.ImageBlock{MediaType: "image/png", Data: png}}},
		{Block: &llmv1.ContentBlock_Document{Document: &llmv1.DocumentBlock{MediaType: "application/pdf", Data: pdf, Title: "a.pdf"}}},
		{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{Text: "what is this"}}},
	}
	if _, err := h.client.SendUserMessage(h.ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{SessionId: sid, Uuid: "u1", Content: content})); err != nil {
		t.Fatal(err)
	}
	got := h.until(ch, isTurnFinished)

	reqs := h.gw.Requests()
	if len(reqs) == 0 {
		t.Fatal("no request reached the gateway")
	}
	var image, document *llm.Source
	for _, m := range reqs[0].Messages {
		for _, b := range m.Content {
			if b.Type == llm.BlockImage {
				image = b.Source
			}
			if b.Type == llm.BlockDocument {
				document = b.Source
			}
		}
	}
	if image == nil || image.MediaType != "image/png" || image.Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("image in the model request = %+v", image)
	}
	if document == nil || document.MediaType != "application/pdf" || document.Data != base64.StdEncoding.EncodeToString(pdf) {
		t.Fatalf("document in the model request = %+v", document)
	}

	var shown *llmv1.ImageBlock
	for _, ev := range got {
		if u := ev.GetUserMessage(); u != nil {
			for _, b := range u.GetContent() {
				if i := b.GetImage(); i != nil {
					shown = i
				}
			}
		}
	}
	if shown == nil || !bytes.Equal(shown.GetData(), png) {
		t.Fatalf("image in the user message event = %v", shown)
	}
}
