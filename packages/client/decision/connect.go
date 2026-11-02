package decision

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	decisionv1 "github.com/xlsft/harness/packages/client/gen/harness/decision/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/decision/v1/decisionv1connect"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/rpc"
)

// Request limits.
const (
	DefaultTimeout     = 10 * time.Second
	MaxUserMessages    = 3
	MaxUserMessageLen  = 2000
	MaxRationaleLength = 2000
)

// Client is the Connect client of harness.decision.v1.DecisionService.
// It implements permissions.Decider.
type Client struct {
	rpc    decisionv1connect.DecisionServiceClient
	tokens rpc.Tokens
	// Timeout bounds one Decide call (default 10 s).
	Timeout time.Duration
	// Provider forces a provider id (admin debugging; "" = configured chain).
	Provider string
	// Git supplies the git facts (default DefaultGitInfo).
	Git GitInfo
}

// NewClient creates the client. httpClient may be nil.
func NewClient(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *Client {
	if httpClient == nil {
		httpClient = rpc.HTTPClient(baseURL)
	}
	return &Client{
		rpc:    decisionv1connect.NewDecisionServiceClient(httpClient, strings.TrimRight(baseURL, "/"), rpc.ClientOptions()...),
		tokens: tokens, Timeout: DefaultTimeout, Git: DefaultGitInfo,
	}
}

// BuildRequest fills a DecideRequest for a call.
func BuildRequest(req permissions.AutoRequest, f Facts, provider string) *decisionv1.DecideRequest {
	input := string(req.Input)
	if input == "" {
		input = "{}"
	}
	return &decisionv1.DecideRequest{
		SessionId:             req.SessionID,
		ToolName:              req.ToolName,
		InputJson:             input,
		Cwd:                   req.Cwd,
		AdditionalDirectories: append([]string(nil), req.AdditionalDirs...),
		RecentUserMessages:    RecentMessages(req.RecentUserMessages),
		AssistantRationale:    truncate(req.Rationale, MaxRationaleLength),
		Facts: &decisionv1.ClientFacts{
			Subcommands:   f.Subcommands,
			WritePaths:    f.WritePaths,
			NetworkHosts:  f.NetworkHosts,
			InsideGitRepo: f.InsideGitRepo,
			GitBranch:     f.GitBranch,
		},
		Provider: provider,
	}
}

// RecentMessages keeps the last MaxUserMessages messages (newest last),
// each truncated to MaxUserMessageLen characters.
func RecentMessages(msgs []string) []string {
	var out []string
	for _, m := range msgs {
		if strings.TrimSpace(m) != "" {
			out = append(out, m)
		}
	}
	if len(out) > MaxUserMessages {
		out = out[len(out)-MaxUserMessages:]
	}
	for i := range out {
		out[i] = truncate(out[i], MaxUserMessageLen)
	}
	return out
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// Decide implements permissions.Decider.
func (c *Client) Decide(ctx context.Context, req permissions.AutoRequest) (permissions.AutoResult, error) {
	f := ComputeFacts(ctx, req, c.Git)
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg := BuildRequest(req, f, c.Provider)
	start := time.Now()
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[decisionv1.DecideResponse], error) {
		r := connect.NewRequest(msg)
		rpc.SetBearer(r.Header(), token)
		return c.rpc.Decide(ctx, r)
	})
	if err != nil {
		// The server gets the timeout too (Connect-Timeout-Ms, rounded down)
		// and may give up a moment before the client's own timer fires.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || pastDeadline(ctx, 10*time.Millisecond) {
			return permissions.AutoResult{}, errors.New("decision service timed out after " + timeout.String())
		}
		return permissions.AutoResult{}, err
	}
	res := FromProto(resp.Msg)
	if res.LatencyMs == 0 {
		res.LatencyMs = time.Since(start).Milliseconds()
	}
	return res, nil
}

// pastDeadline reports whether ctx's deadline is less than grace away.
func pastDeadline(ctx context.Context, grace time.Duration) bool {
	dl, ok := ctx.Deadline()
	return ok && time.Until(dl) < grace
}

// ListProviders returns the server's provider chain.
func (c *Client) ListProviders(ctx context.Context) (*decisionv1.ListProvidersResponse, error) {
	resp, err := rpc.WithAuth(ctx, c.tokens, func(token string) (*connect.Response[decisionv1.ListProvidersResponse], error) {
		r := connect.NewRequest(&decisionv1.ListProvidersRequest{})
		rpc.SetBearer(r.Header(), token)
		return c.rpc.ListProviders(ctx, r)
	})
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// FromProto converts a DecideResponse. DECISION_UNSPECIFIED becomes ASK.
func FromProto(r *decisionv1.DecideResponse) permissions.AutoResult {
	out := permissions.AutoResult{
		Reason: r.GetReason(), Provider: r.GetProvider(), Confidence: r.GetConfidence(),
		Cached: r.GetCached(), LatencyMs: r.GetLatencyMs(), CostUSD: r.GetCostUsd(),
	}
	switch r.GetDecision() {
	case decisionv1.Decision_DECISION_ALLOW:
		out.Behavior = permissions.Allow
	case decisionv1.Decision_DECISION_DENY:
		out.Behavior = permissions.Deny
	default:
		out.Behavior = permissions.Ask
	}
	for _, c := range r.GetCategories() {
		if c == decisionv1.RiskCategory_RISK_CATEGORY_UNSPECIFIED {
			continue
		}
		out.Categories = append(out.Categories, CategoryName(c))
	}
	return out
}

// CategoryName maps RISK_CATEGORY_SECRET_EXFILTRATION to "secret_exfiltration".
func CategoryName(c decisionv1.RiskCategory) string {
	return strings.ToLower(strings.TrimPrefix(c.String(), "RISK_CATEGORY_"))
}

// CategoryFromName is the inverse of CategoryName (UNSPECIFIED when unknown).
func CategoryFromName(s string) decisionv1.RiskCategory {
	return decisionv1.RiskCategory(decisionv1.RiskCategory_value["RISK_CATEGORY_"+strings.ToUpper(s)])
}
