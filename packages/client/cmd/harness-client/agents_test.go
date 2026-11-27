package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

type agentsFake struct {
	relayv1connect.UnimplementedInboxServiceHandler
	auth []string
}

func (f *agentsFake) ListInbox(ctx context.Context, req *connect.Request[relayv1.ListInboxRequest]) (*connect.Response[relayv1.ListInboxResponse], error) {
	f.auth = append(f.auth, req.Header().Get("Authorization"))
	var items []*agentv1.InboxItem
	if req.Msg.GetRequest().GetGroup() == agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT {
		off := false
		items = []*agentv1.InboxItem{{SessionId: "s1", Title: "Fix the build", DeviceName: "laptop", DeviceOnline: &off, Unread: true,
			Group: agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT, Requests: []*agentv1.InboxRequest{{Kind: agentv1.InboxRequest_KIND_PERMISSION, ToolName: "Bash", Summary: "make"}}}}
	}
	return connect.NewResponse(&relayv1.ListInboxResponse{Response: &agentv1.ListInboxResponse{Items: items,
		Counts: []*agentv1.InboxCount{{Group: agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT, Total: 1, Unread: 1}}}}), nil
}

type staticTokens string

func (s staticTokens) Token(context.Context) (string, error)   { return string(s), nil }
func (s staticTokens) Refresh(context.Context) (string, error) { return string(s), nil }

func TestAgentsList(t *testing.T) {
	fake := &agentsFake{}
	mux := http.NewServeMux()
	mux.Handle(relayv1connect.NewInboxServiceHandler(fake))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := agentsClient{rpc: relayv1connect.NewInboxServiceClient(http.DefaultClient, srv.URL, rpc.ClientOptions()...), tokens: staticTokens("hat_x")}
	var out bytes.Buffer
	groups := []agentv1.InboxGroup{agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT, agentv1.InboxGroup_INBOX_GROUP_RUNNING}
	if err := c.list(context.Background(), &out, groups, 10, false); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"Needs you (1)", "* s1", "laptop (offline)", "Fix the build", "allow Bash: make", "Running (0)"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if fake.auth[0] != "Bearer hat_x" {
		t.Fatalf("auth %v", fake.auth)
	}
	out.Reset()
	if err := c.list(context.Background(), &out, groups[:1], 10, true); err != nil || !strings.Contains(out.String(), `"sessionId":"s1"`) {
		t.Fatalf("json %q %v", out.String(), err)
	}
}
