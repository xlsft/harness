package server

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func TestHooksPreference(t *testing.T) {
	ctx := context.Background()
	s := &Server{}
	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil || !got.Msg.Preferences.GetHooksEnabled() {
		t.Fatalf("hooks run by default: %v %+v", err, got)
	}
	docs := &fakeDocs{}
	s.Preferences = docs
	docs.write(`{"model":"big"}`)
	off := false
	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{HooksEnabled: &off}))
	if err != nil || up.Msg.Preferences.GetHooksEnabled() {
		t.Fatalf("update: %v %+v", err, up)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil {
		t.Fatal(err)
	}
	if stored["disableAllHooks"] != true || stored["model"] != "big" {
		t.Fatalf("stored: %v", stored)
	}
	// Another preference leaves it alone.
	up, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{LspEnabled: &off}))
	if err != nil || up.Msg.Preferences.GetHooksEnabled() {
		t.Fatalf("other: %v %+v", err, up)
	}
	on := true
	up, err = s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{HooksEnabled: &on}))
	if err != nil || !up.Msg.Preferences.GetHooksEnabled() {
		t.Fatalf("on again: %v %+v", err, up)
	}
}

// refDocs serves documents by what the request asks for.
type refDocs struct{ docs map[string]*syncv1.Document }

func (d refDocs) GetDocument(_ context.Context, req *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error) {
	r := req.GetRef()
	doc := d.docs[r.GetScope().String()+r.GetKind().String()]
	if doc == nil {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return &syncv1.GetDocumentResponse{Document: doc}, nil
}

func (d refDocs) PutDocument(context.Context, *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}

// What the server delivers does not run until this device says so, and only that exact content.
func TestHooksFromTheServerWaitForThisDevice(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARNESS_NO_HOOKS", "")
	ctx := context.Background()
	hook := func(command string) *syncv1.Document {
		return &syncv1.Document{Id: "doc_h", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_HOOK, Key: "user",
			Content: `{"Stop":[{"hooks":[{"type":"command","command":"` + command + `"}]}]}`}
	}
	docs := refDocs{docs: map[string]*syncv1.Document{"SCOPE_USERDOCUMENT_KIND_HOOK": hook("first")}}
	s := &Server{Preferences: docs}
	list := func() *agentv1.ConfiguredHook {
		t.Helper()
		res, err := s.ListHooks(ctx, connect.NewRequest(&agentv1.ListHooksRequest{}))
		if err != nil || len(res.Msg.Hooks) != 1 {
			t.Fatalf("list: %v %+v", err, res)
		}
		return res.Msg.Hooks[0]
	}
	h := list()
	if h.Status != agentv1.ConfiguredHook_STATUS_NEEDS_DEVICE_TRUST || h.TrustHash == "" || h.Source != "user" {
		t.Fatalf("before: %+v", h)
	}
	if _, err := s.SetHooksTrust(ctx, connect.NewRequest(&agentv1.SetHooksTrustRequest{Hash: h.TrustHash, Trusted: true})); err != nil {
		t.Fatal(err)
	}
	if got := list(); got.Status != agentv1.ConfiguredHook_STATUS_ACTIVE {
		t.Fatalf("trusted: %+v", got)
	}
	// The server changes it: trust does not carry over.
	docs.docs["SCOPE_USERDOCUMENT_KIND_HOOK"] = hook("second")
	if got := list(); got.Status != agentv1.ConfiguredHook_STATUS_NEEDS_DEVICE_TRUST || got.TrustHash == h.TrustHash {
		t.Fatalf("changed: %+v", got)
	}
	if _, err := s.SetHooksTrust(ctx, connect.NewRequest(&agentv1.SetHooksTrustRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty hash: %v", err)
	}
}
