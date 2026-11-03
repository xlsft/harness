package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/gateway"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/localmodels"
)

type coreOnly struct{}

func (coreOnly) Stream(context.Context, *llm.Request) (gateway.EventStream, error) {
	return nil, errors.New("no core in this test")
}

func (coreOnly) Models(context.Context) (*llm.Catalog, error) {
	return &llm.Catalog{Models: []llm.Model{{ID: "glm-4.6", Provider: "OpenRouter"}}, Slots: map[string]string{"main": "glm-4.6"}}, nil
}

func (coreOnly) CountTokens(context.Context, *llm.Request) (*llm.TokenCount, error) {
	return &llm.TokenCount{}, nil
}

func fakeOllamaServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"0.12.3"}`) })
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"qwen3:8b","size":5},{"name":"llama3.2:3b","size":3}]}`)
	})
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model_info":{"general.architecture":"qwen3","qwen3.context_length":40960},"capabilities":["completion","tools"]}`)
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"downloading","digest":"sha256:1","total":10,"completed":10}`)
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	return httptest.NewServer(mux)
}

func TestLocalProviderRPCs(t *testing.T) {
	ollama := fakeOllamaServer()
	defer ollama.Close()
	reg, err := localmodels.New(localmodels.Options{Path: filepath.Join(t.TempDir(), "lp.json"), NoBuiltins: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(bootstrap.Options{Gateway: &localmodels.Router{Core: coreOnly{}, Local: reg}})
	defer srv.Close()
	srv.LocalModels = reg
	mux := http.NewServeMux()
	path, h := srv.Handler()
	mux.Handle(path, h)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := agentv1connect.NewAgentServiceClient(ts.Client(), ts.URL)
	ctx := context.Background()

	added, err := c.AddLocalProvider(ctx, connect.NewRequest(&agentv1.AddLocalProviderRequest{Kind: agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_OLLAMA, Name: "Box", BaseUrl: ollama.URL}))
	if err != nil {
		t.Fatal(err)
	}
	p := added.Msg.Provider
	if !p.Reachable || p.Version != "0.12.3" || len(p.Models) != 2 || p.Kind != agentv1.LocalProviderKind_LOCAL_PROVIDER_KIND_OLLAMA || p.CheckedAt == "" {
		t.Fatalf("added = %+v", p)
	}
	if m := p.Models[1]; m.Id != "local:"+p.Id+"/qwen3:8b" || m.GetTools() != true || m.MaxContextWindow != 40960 {
		t.Errorf("model = %+v", m)
	}

	// The model picker's list: the server's models, then the local ones (marked).
	init, err := c.Initialize(ctx, connect.NewRequest(&agentv1.InitializeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(init.Msg.Models) != 3 || init.Msg.Models[0].Local || !init.Msg.Models[2].Local || init.Msg.Models[2].Provider != "Box" || init.Msg.DefaultModel != "glm-4.6" {
		t.Errorf("models = %+v", init.Msg.Models)
	}

	stream, err := c.PullOllamaModel(ctx, connect.NewRequest(&agentv1.PullOllamaModelRequest{ProviderId: p.Id, Model: "phi4:14b"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []*agentv1.PullOllamaModelResponse
	for stream.Receive() {
		got = append(got, stream.Msg())
	}
	if err := stream.Err(); err != nil || len(got) != 3 || !got[2].Done || got[1].Completed != 10 {
		t.Errorf("pull = %v %+v", err, got)
	}

	off := false
	up, err := c.UpdateLocalProvider(ctx, connect.NewRequest(&agentv1.UpdateLocalProviderRequest{Id: p.Id, Enabled: &off}))
	if err != nil || up.Msg.Provider.Enabled || len(up.Msg.Provider.Models) != 0 {
		t.Errorf("update = %v %+v", err, up)
	}
	if _, err := c.UpdateLocalProvider(ctx, connect.NewRequest(&agentv1.UpdateLocalProviderRequest{Id: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("update unknown = %v", err)
	}
	if _, err := c.AddLocalProvider(ctx, connect.NewRequest(&agentv1.AddLocalProviderRequest{BaseUrl: "not a url"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("add bad url = %v", err)
	}

	reg.SetPolicyDisabled(true)
	list, err := c.ListLocalProviders(ctx, connect.NewRequest(&agentv1.ListLocalProvidersRequest{}))
	if err != nil || !list.Msg.DisabledByPolicy || len(list.Msg.Providers) != 1 {
		t.Errorf("list = %v %+v", err, list)
	}
	if _, err := c.DeleteOllamaModel(ctx, connect.NewRequest(&agentv1.DeleteOllamaModelRequest{ProviderId: p.Id, Model: "x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("delete under policy = %v", err)
	}
	reg.SetPolicyDisabled(false)

	if _, err := c.RemoveLocalProvider(ctx, connect.NewRequest(&agentv1.RemoveLocalProviderRequest{Id: p.Id})); err != nil {
		t.Fatal(err)
	}
	list, _ = c.ListLocalProviders(ctx, connect.NewRequest(&agentv1.ListLocalProvidersRequest{}))
	if len(list.Msg.Providers) != 0 {
		t.Errorf("after remove = %+v", list.Msg.Providers)
	}
}
