package server

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/clientlog"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Diagnostics: what the model of a session is offered (ListSessionTools, /tools) and a report of this client for a bug
// report (GetDiagnostics). Neither carries tokens, keys or prompt text.

func sessionToolsProto(list []bootstrap.ToolStatus) []*agentv1.SessionTool {
	out := make([]*agentv1.SessionTool, 0, len(list))
	for _, t := range list {
		out = append(out, &agentv1.SessionTool{Name: t.Name, Source: t.Source, Offered: t.Offered, Reason: t.Reason, Detail: t.Detail})
	}
	return out
}

// ListSessionTools implements AgentService.ListSessionTools.
func (s *Server) ListSessionTools(_ context.Context, req *connect.Request[agentv1.ListSessionToolsRequest]) (*connect.Response[agentv1.ListSessionToolsResponse], error) {
	ls, err := s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.ListSessionToolsResponse{
		Tools: sessionToolsProto(ls.sess.ToolReport()), SyncOff: ls.sess.SyncOff, PromptBundleVersion: ls.sess.Agent.Bundle().Version,
	}), nil
}

func runTools(_ context.Context, _ *Server, ls *liveSession, _ string) slashOutcome {
	text := bootstrap.FormatToolReport(ls.sess.ToolReport())
	return slashOutcome{text: strings.TrimRight(text, "\n") + "\n\n" + ls.sess.SyncState()}
}

// diagnosticsLogLines is how much of the end of the log a report carries.
const diagnosticsLogLines = 80

// GetDiagnostics implements AgentService.GetDiagnostics.
func (s *Server) GetDiagnostics(ctx context.Context, _ *connect.Request[agentv1.GetDiagnosticsRequest]) (*connect.Response[agentv1.GetDiagnosticsResponse], error) {
	var b strings.Builder
	fmt.Fprintf(&b, "xlsft's harness diagnostics, %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "client: %s\n", config.UserAgent())
	fmt.Fprintf(&b, "platform: %s/%s, %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	server := "none"
	if u, err := url.Parse(s.Base.ServerURL); err == nil && u.Host != "" {
		server = u.Scheme + "://" + u.Host
	}
	if s.ServerVersion != nil && server != "none" {
		vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if v := s.ServerVersion(vctx); v != "" {
			server += " (" + v + ")"
		}
		cancel()
	}
	fmt.Fprintf(&b, "server: %s, signed in: %v\n", server, s.Base.Tokens != nil)
	fmt.Fprintf(&b, "logs: %s\n", clientlog.Dir())

	s.mu.Lock()
	live := make([]*liveSession, 0, len(s.live))
	for _, ls := range s.live {
		live = append(live, ls)
	}
	s.mu.Unlock()
	sort.Slice(live, func(i, j int) bool { return live[i].sess.ID < live[j].sess.ID })
	fmt.Fprintf(&b, "\nopen sessions: %d\n", len(live))
	for _, ls := range live {
		sess := ls.sess
		var off []string
		offered := 0
		for _, t := range sess.ToolReport() {
			if t.Offered {
				offered++
			} else {
				off = append(off, t.Name+" ("+t.Reason+")")
			}
		}
		place := "folder"
		if isNoFolder(sess.ID, sess.Dir()) {
			place = "no folder"
		}
		fmt.Fprintf(&b, "- %s: %s, %s, bundle %s, %s; %d tools offered", sess.ID, sess.Agent.Model(), place, sess.Agent.Bundle().Version, sess.SyncState(), offered)
		if len(off) > 0 {
			fmt.Fprintf(&b, "; not offered: %s", strings.Join(off, ", "))
		}
		b.WriteString("\n")
	}
	if tail := clientlog.Tail(diagnosticsLogLines); tail != "" {
		fmt.Fprintf(&b, "\nend of %s:\n%s\n", clientlog.FileName, clientlog.Redact(tail))
	}
	return connect.NewResponse(&agentv1.GetDiagnosticsResponse{Text: b.String(), LogsDir: clientlog.Dir()}), nil
}
