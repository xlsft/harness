package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/rpc"
)

const agentsUsage = `Usage: harness-client agents [flags]
       harness-client agents read <session id>...

Lists the inbox from the signed-in server: every session and background agent of
yours on every machine that mirrors its sessions, grouped by what it needs from
you (docs/design/coordinator.md). Answer in the app or the session itself.

Flags:
`

// agentGroups maps --group to the inbox groups, in the order they print.
var agentGroups = []struct {
	flag, title string
	group       agentv1.InboxGroup
}{
	{"needs", "Needs you", agentv1.InboxGroup_INBOX_GROUP_NEEDS_INPUT},
	{"failed", "Failed", agentv1.InboxGroup_INBOX_GROUP_FAILED},
	{"finished", "Finished", agentv1.InboxGroup_INBOX_GROUP_FINISHED},
	{"running", "Running", agentv1.InboxGroup_INBOX_GROUP_RUNNING},
}

// runAgents implements `harness-client agents`.
func runAgents(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := pflag.NewFlagSet("agents", pflag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, agentsUsage); fs.PrintDefaults() }
	group := fs.String("group", "", "only one group: needs, failed, finished or running")
	asJSON := fs.Bool("json", false, "one JSON item per line")
	watch := fs.Bool("watch", false, "keep printing changes until interrupted")
	limit := fs.Int("limit", 50, "items per group")
	srv := fs.String("server", "", "harness server URL (defaults to the signed-in server)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	tokens, url, _, err := bootstrap.Tokens(*srv)
	if err != nil {
		return fail(err)
	}
	c := agentsClient{rpc: relayv1connect.NewInboxServiceClient(rpc.HTTPClient(url), strings.TrimRight(url, "/"), rpc.ClientOptions()...), tokens: tokens}
	if rest := fs.Args(); len(rest) > 0 {
		if rest[0] != "read" || len(rest) < 2 {
			return fail(errors.New("usage: harness-client agents read <session id>..."))
		}
		if err := c.markRead(ctx, rest[1:]); err != nil {
			return fail(err)
		}
		return 0
	}
	if *watch {
		return c.watch(ctx, os.Stdout, *asJSON)
	}
	var groups []agentv1.InboxGroup
	for _, g := range agentGroups {
		if *group == "" || *group == g.flag {
			groups = append(groups, g.group)
		}
	}
	if len(groups) == 0 {
		return fail(fmt.Errorf("unknown group %q (needs, failed, finished, running)", *group))
	}
	if err := c.list(ctx, os.Stdout, groups, *limit, *asJSON); err != nil {
		return fail(err)
	}
	return 0
}

type agentsClient struct {
	rpc    relayv1connect.InboxServiceClient
	tokens rpc.Tokens
}

func (c agentsClient) bearer(ctx context.Context, h interface{ Set(string, string) }, refresh bool) error {
	tok, err := c.tokens.Token(ctx)
	if refresh {
		tok, err = c.tokens.Refresh(ctx)
	}
	if err != nil {
		return err
	}
	h.Set("Authorization", "Bearer "+tok)
	return nil
}

func (c agentsClient) page(ctx context.Context, g agentv1.InboxGroup, size int) (*agentv1.ListInboxResponse, error) {
	for attempt := 0; ; attempt++ {
		req := connect.NewRequest(&relayv1.ListInboxRequest{Request: &agentv1.ListInboxRequest{Group: g, PageSize: int32(size)}})
		if err := c.bearer(ctx, req.Header(), attempt > 0); err != nil {
			return nil, err
		}
		res, err := c.rpc.ListInbox(ctx, req)
		if err == nil {
			return res.Msg.GetResponse(), nil
		}
		if connect.CodeOf(err) != connect.CodeUnauthenticated || attempt > 0 {
			return nil, err
		}
	}
}

func (c agentsClient) list(ctx context.Context, w io.Writer, groups []agentv1.InboxGroup, limit int, asJSON bool) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, g := range groups {
		res, err := c.page(ctx, g, limit)
		if err != nil {
			return err
		}
		if asJSON {
			for _, it := range res.GetItems() {
				b, _ := protojson.Marshal(it)
				fmt.Fprintln(w, string(b))
			}
			continue
		}
		total := 0
		for _, n := range res.GetCounts() {
			if n.GetGroup() == g {
				total = int(n.GetTotal())
			}
		}
		fmt.Fprintf(tw, "%s (%d)\n", groupTitle(g), total)
		for _, it := range res.GetItems() {
			fmt.Fprintln(tw, agentLine(it))
		}
		if len(res.GetItems()) == 0 {
			fmt.Fprintln(tw, "  -")
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

func (c agentsClient) watch(ctx context.Context, w io.Writer, asJSON bool) int {
	req := connect.NewRequest(&relayv1.WatchInboxRequest{})
	if err := c.bearer(ctx, req.Header(), false); err != nil {
		return fail(err)
	}
	st, err := c.rpc.WatchInbox(ctx, req)
	if err != nil {
		return fail(err)
	}
	for st.Receive() {
		r := st.Msg().GetResponse()
		ch := r.GetChange()
		if ch == nil {
			if r.GetResync() {
				fmt.Fprintln(w, "(fell behind: run `harness-client agents` again)")
			}
			continue
		}
		if asJSON {
			b, _ := protojson.Marshal(ch)
			fmt.Fprintln(w, string(b))
			continue
		}
		if id := ch.GetRemovedSessionId(); id != "" {
			fmt.Fprintf(w, "- %s left the inbox\n", id)
			continue
		}
		fmt.Fprintf(w, "%s:%s\n", groupTitle(ch.GetItem().GetGroup()), agentLine(ch.GetItem()))
	}
	if err := st.Err(); err != nil && ctx.Err() == nil {
		return fail(err)
	}
	return 0
}

func (c agentsClient) markRead(ctx context.Context, ids []string) error {
	req := connect.NewRequest(&relayv1.MarkInboxReadRequest{Request: &agentv1.MarkInboxReadRequest{SessionIds: ids, Read: true}})
	if err := c.bearer(ctx, req.Header(), false); err != nil {
		return err
	}
	_, err := c.rpc.MarkInboxRead(ctx, req)
	return err
}

func groupTitle(g agentv1.InboxGroup) string {
	for _, x := range agentGroups {
		if x.group == g {
			return x.title
		}
	}
	return "Other"
}

// agentLine is one tab-separated row: unread mark, session, machine, title and what it needs or said.
func agentLine(it *agentv1.InboxItem) string {
	mark := " "
	if it.GetUnread() {
		mark = "*"
	}
	device := it.GetDeviceName()
	if device == "" {
		device = it.GetDeviceId()
	}
	if it.DeviceOnline != nil && !it.GetDeviceOnline() {
		device += " (offline)"
	}
	what := it.GetReport()
	switch {
	case len(it.GetRequests()) > 0:
		r := it.GetRequests()[0]
		switch r.GetKind() {
		case agentv1.InboxRequest_KIND_PERMISSION:
			what = "allow " + r.GetToolName() + ": " + r.GetSummary()
		case agentv1.InboxRequest_KIND_QUESTION:
			if q := r.GetQuestions(); len(q) > 0 {
				what = "question: " + q[0].GetQuestion()
			}
		case agentv1.InboxRequest_KIND_PLAN:
			what = "plan to approve"
		case agentv1.InboxRequest_KIND_PROJECT_RUN:
			what = "project run paused: " + r.GetSummary()
		}
	case it.GetError() != "":
		what = it.GetError()
	case it.GetWorking():
		what = "working"
	}
	for _, a := range it.GetAgents() {
		if a.GetState() == agentv1.SubagentState_SUBAGENT_STATE_RUNNING {
			what += fmt.Sprintf(" [agent %s running]", a.GetName())
		}
	}
	return fmt.Sprintf("%s %s\t%s\t%s\t%s", mark, it.GetSessionId(), device, cutCLI(it.GetTitle(), 50), cutCLI(what, 90))
}

func cutCLI(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
