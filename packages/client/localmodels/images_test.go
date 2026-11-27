package localmodels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/images"
)

var localPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d}

func TestLocalImageGeneration(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"sd-turbo"}]}`)
			return
		}
		gotPath, gotAuth, gotCT = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(localPNG)}}})
	}))
	defer srv.Close()
	r := newTestRegistry(t)
	p := addProvider(t, r, KindCustom, srv.URL+"/v1", "sk-local-fake")
	id := ModelID(p.ID, "sd-turbo")
	router := &Router{Local: r}

	res, err := router.GenerateImages(context.Background(), images.Request{Model: id, Prompt: "a fox", Size: "512x512", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 || res.Images[0].MediaType != "image/png" {
		t.Fatalf("images = %+v", res.Images)
	}
	var body map[string]any
	_ = json.Unmarshal(gotBody, &body)
	if gotPath != "/v1/images/generations" || gotAuth != "Bearer sk-local-fake" || body["model"] != "sd-turbo" || body["size"] != "512x512" || body["response_format"] != "b64_json" {
		t.Fatalf("request %s %s %v", gotPath, gotAuth, body)
	}

	// Edits go as multipart.
	if _, err := router.GenerateImages(context.Background(), images.Request{Model: id, Prompt: "blue", Count: 1, Inputs: []images.Input{{Data: localPNG, MediaType: "image/png", Name: "in.png"}}}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/images/edits" || !strings.HasPrefix(gotCT, "multipart/form-data") || !strings.Contains(string(gotBody), `name="image[]"; filename="in.png"`) {
		t.Fatalf("edit request %s %s", gotPath, gotCT)
	}

	// The server's policy turns local image models off too.
	r.SetPolicyDisabled(true)
	if _, err := router.GenerateImages(context.Background(), images.Request{Model: id, Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("policy ignored: %v", err)
	}
	r.SetPolicyDisabled(false)

	// Ollama has no image API; unknown providers are refused.
	o := addProvider(t, r, KindOllama, srv.URL, "")
	if _, _, err := r.ImageProvider(ModelID(o.ID, "x")); err == nil || !strings.Contains(err.Error(), "Ollama") {
		t.Fatalf("ollama accepted: %v", err)
	}
	if _, _, err := r.ImageProvider("local:nope/x"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
