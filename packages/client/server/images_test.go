package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
)

var imageToolPrompt = prompts.ToolPrompt{Description: "Makes images.", InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"file_path":{"type":"string"}}}`)}

func TestImageGenerateIsOfferedOnlyWithAnImageModel(t *testing.T) {
	h := startHooked(t, fg.Responder(func(int, *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m", fg.Text("ok"))} }), []byte(`{}`))
	h.gw.Bundle.Tools["ImageGenerate"] = imageToolPrompt
	// An image model exists but nobody chose it and there is no server default: no tool.
	h.gw.ImageModels = []string{"img-std"}
	sid, ch := h.create(&agentv1.CreateSessionRequest{})
	h.send(sid, "hi")
	h.until(ch, isTurnFinished)
	if slices.Contains(offered(h.gw.Requests()[0]), "ImageGenerate") {
		t.Fatal("ImageGenerate offered without an image model")
	}
}

func TestImageGenerateWritesTheServerModelsImage(t *testing.T) {
	var target string
	respond := fg.Sequence(
		func(r *llm.Request) fg.Reply {
			target = filepath.Join(fg.Cwd(r), "assets", "fox.png")
			return fg.Reply{Events: fg.Message("m1", fg.ToolUse("g1", "ImageGenerate", map[string]any{"prompt": "a fox", "file_path": target, "count": 1}))}
		},
		func(r *llm.Request) fg.Reply { return fg.Reply{Events: fg.Message("m2", fg.Text("Done."))} },
	)
	// The user's own choice goes over the server default.
	h := startHooked(t, respond, []byte(`{"imageModel":"img-mine"}`))
	h.gw.Bundle.Tools["ImageGenerate"] = imageToolPrompt
	h.gw.ImageModels = []string{"img-std", "img-mine"}
	h.gw.ImageDefault = "img-std"
	sid, ch := h.create(&agentv1.CreateSessionRequest{PermissionMode: agentv1.PermissionMode_PERMISSION_MODE_ACCEPT_EDITS})
	h.send(sid, "draw a fox")
	h.until(ch, isTurnFinished)
	if !slices.Contains(offered(h.gw.Requests()[0]), "ImageGenerate") {
		t.Fatalf("no ImageGenerate tool: %v", offered(h.gw.Requests()[0]))
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != string(fg.TinyPNG) {
		t.Fatalf("image not written: %v", err)
	}
	reqs := h.gw.ImageRequests()
	if len(reqs) != 1 || reqs[0].GetModel() != "img-mine" || reqs[0].GetPrompt() != "a fox" || reqs[0].GetMetadata().GetSessionId() != sid {
		t.Fatalf("image requests = %v", reqs)
	}
	if got := results(h)["g1"]; !strings.Contains(got, "Generated 1 image") {
		t.Fatalf("result: %q", got)
	}
}
