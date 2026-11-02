// Package fakegateway is a scripted harness server for tests and local
// end-to-end runs. It serves GatewayService, PromptService and
// DeviceAuthService and DecisionService from the generated Connect handlers
// over h2c.
package fakegateway

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	authv1 "github.com/xlsft/harness/packages/client/gen/harness/auth/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/auth/v1/authv1connect"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/decision/v1/decisionv1connect"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/llm/v1/llmv1connect"
	promptsv1 "github.com/xlsft/harness/packages/client/gen/harness/prompts/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/prompts/v1/promptsv1connect"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/rpc"
)

//go:embed bundle.json
var bundleJSON []byte

// DefaultBundle returns the test prompt bundle.
func DefaultBundle() *prompts.Bundle {
	var b prompts.Bundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		panic(err)
	}
	return &b
}

// Reply is one scripted model response.
type Reply struct {
	Events []*llmv1.StreamMessageResponse
	// Err ends the stream with this error after Events.
	Err error
	// Hold keeps the stream open after Events until the client cancels.
	Hold bool
}

// Responder produces the reply for the n-th (0-based) messages request.
type Responder func(n int, req *llm.Request) Reply

// Server is the fake harness server.
type Server struct {
	Bundle *prompts.Bundle
	// Respond scripts the model; defaults to a single "ok" text reply.
	Respond Responder
	// Model ids in the catalog; the first is the main slot.
	Models []string
	// SmallModel is the catalog's small slot ("" leaves it unset).
	SmallModel string
	// Title scripts session title requests (see IsTitleRequest). They are
	// recorded apart (TitleRequests) and do not advance Respond's request
	// number. When nil, title requests fail.
	Title func(req *llm.Request) Reply

	// RejectTokens makes the next N authenticated calls fail with UNAUTHENTICATED.
	RejectTokens atomic.Int32
	// AccessToken is the token issued on login/refresh.
	AccessToken string

	mu       sync.Mutex
	requests []*llm.Request
	titles   []*llm.Request
	bundleN  int
	notMod   int
	refreshN int
	polls    int

	decision decisionState
}

// New returns a server with the default bundle.
func New(respond Responder) *Server {
	return &Server{Bundle: DefaultBundle(), Respond: respond, Models: []string{"fake-model"}, AccessToken: "hat_test"}
}

// Requests returns the recorded messages requests.
func (s *Server) Requests() []*llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*llm.Request(nil), s.requests...)
}

// TitleRequests returns the recorded title requests.
func (s *Server) TitleRequests() []*llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*llm.Request(nil), s.titles...)
}

// IsTitleRequest reports whether req asks for a session title: an
// auxiliary request whose system prompt is the bundle's title instruction.
func (s *Server) IsTitleRequest(req *llm.Request) bool {
	if req.Metadata == nil || req.Metadata.RequestClass != "auxiliary" || s.Bundle == nil || s.Bundle.Title == nil || s.Bundle.Title.Instruction == "" {
		return false
	}
	for _, b := range req.System {
		if strings.Contains(b.Text, s.Bundle.Title.Instruction) {
			return true
		}
	}
	return false
}

// EchoTitle is a Title script that names the session after the first words
// of the conversation it is given.
func EchoTitle(req *llm.Request) Reply {
	text := ""
	if len(req.Messages) > 0 {
		for _, b := range req.Messages[0].Content {
			text += b.Text
		}
	}
	text = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, "<conversation>")), "User:")
	w := strings.Fields(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if len(w) > 5 {
		w = w[:5]
	}
	return Reply{Events: Message("msg_title", Text(strings.Join(w, " ")))}
}

// Stats returns (bundle fetches, not-modified answers, refreshes).
func (s *Server) Stats() (bundles, notModified, refreshes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bundleN, s.notMod, s.refreshN
}

// Handler returns the HTTP handler for all services.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(llmv1connect.NewGatewayServiceHandler(gatewayHandler{s: s}))
	mux.Handle(promptsv1connect.NewPromptServiceHandler(promptHandler{s: s}))
	mux.Handle(authv1connect.NewDeviceAuthServiceHandler(authHandler{s: s}))
	mux.Handle(decisionv1connect.NewDecisionServiceHandler(decisionHandler{s: s}))
	return mux
}

// HTTPServer returns an h2c-capable http.Server for the handler.
func (s *Server) HTTPServer() *http.Server {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return &http.Server{Handler: s.Handler(), Protocols: p}
}

// Listen starts serving on addr (e.g. "127.0.0.1:0") and returns the base URL.
func (s *Server) Listen(addr string) (string, func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	srv := s.HTTPServer()
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }, nil
}

func (s *Server) checkAuth(h http.Header) error {
	if s.RejectTokens.Load() > 0 {
		s.RejectTokens.Add(-1)
		return rpc.NewError(connect.CodeUnauthenticated, commonv1.ErrorType_ERROR_TYPE_UNAUTHENTICATED, "token expired", 0)
	}
	if !strings.HasPrefix(h.Get("Authorization"), "Bearer hat_") {
		return rpc.NewError(connect.CodeUnauthenticated, commonv1.ErrorType_ERROR_TYPE_UNAUTHENTICATED, "missing token", 0)
	}
	return nil
}

// ---- GatewayService ----

type gatewayHandler struct {
	llmv1connect.UnimplementedGatewayServiceHandler
	s *Server
}

func (g gatewayHandler) ListModels(ctx context.Context, req *connect.Request[llmv1.ListModelsRequest]) (*connect.Response[llmv1.ListModelsResponse], error) {
	if err := g.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	resp := &llmv1.ListModelsResponse{Slots: &llmv1.Slots{}}
	for i, id := range g.s.Models {
		resp.Models = append(resp.Models, &llmv1.Model{Id: id, DisplayName: "Fake " + id, ContextWindow: 200000, MaxOutputTokens: 16000,
			Capabilities: &llmv1.Capabilities{ParallelToolCalls: true, PromptCache: true}})
		if i == 0 {
			resp.Slots.Main = id
		}
	}
	resp.Slots.Small = g.s.SmallModel
	return connect.NewResponse(resp), nil
}

func (g gatewayHandler) CountTokens(ctx context.Context, req *connect.Request[llmv1.CountTokensRequest]) (*connect.Response[llmv1.CountTokensResponse], error) {
	if err := g.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	n := 0
	for _, m := range req.Msg.GetMessages() {
		n += len(m.String()) / 4
	}
	return connect.NewResponse(&llmv1.CountTokensResponse{InputTokens: int64(n), Estimated: true}), nil
}

func (g gatewayHandler) StreamMessage(ctx context.Context, req *connect.Request[llmv1.StreamMessageRequest], stream *connect.ServerStream[llmv1.StreamMessageResponse]) error {
	if err := g.s.checkAuth(req.Header()); err != nil {
		return err
	}
	r := pbconv.FromStreamRequest(req.Msg)
	if md := req.Msg.GetMetadata(); md != nil && r.Metadata != nil {
		classes := map[llmv1.RequestClass]string{llmv1.RequestClass_REQUEST_CLASS_MAIN: "main", llmv1.RequestClass_REQUEST_CLASS_SUBAGENT: "subagent",
			llmv1.RequestClass_REQUEST_CLASS_COMPACTION: "compaction", llmv1.RequestClass_REQUEST_CLASS_AUXILIARY: "auxiliary"}
		r.Metadata.RequestClass = classes[md.GetRequestClass()]
	}
	if tc := req.Msg.GetToolChoice(); tc != nil {
		modes := map[llmv1.ToolChoice_Mode]string{llmv1.ToolChoice_MODE_AUTO: "auto", llmv1.ToolChoice_MODE_ANY: "any", llmv1.ToolChoice_MODE_TOOL: "tool", llmv1.ToolChoice_MODE_NONE: "none"}
		r.ToolChoice = &llm.ToolChoice{Type: modes[tc.GetMode()], Name: tc.GetToolName()}
	}
	if g.s.IsTitleRequest(r) {
		g.s.mu.Lock()
		g.s.titles = append(g.s.titles, r)
		title := g.s.Title
		g.s.mu.Unlock()
		if title == nil {
			return rpc.NewError(connect.CodeInvalidArgument, commonv1.ErrorType_ERROR_TYPE_INVALID_ARGUMENT, "no title scripted", 0)
		}
		return g.s.play(ctx, stream, title(r))
	}
	g.s.mu.Lock()
	n := len(g.s.requests)
	g.s.requests = append(g.s.requests, r)
	g.s.mu.Unlock()
	respond := g.s.Respond
	if respond == nil {
		respond = func(int, *llm.Request) Reply { return Reply{Events: Message("msg_ok", Text("ok"))} }
	}
	return g.s.play(ctx, stream, respond(n, r))
}

func (s *Server) play(ctx context.Context, stream *connect.ServerStream[llmv1.StreamMessageResponse], reply Reply) error {
	for _, ev := range reply.Events {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if reply.Hold {
		<-ctx.Done()
		return ctx.Err()
	}
	return reply.Err
}

// ---- PromptService ----

type promptHandler struct {
	promptsv1connect.UnimplementedPromptServiceHandler
	s *Server
}

func (p promptHandler) GetBundle(ctx context.Context, req *connect.Request[promptsv1.GetBundleRequest]) (*connect.Response[promptsv1.GetBundleResponse], error) {
	if err := p.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	p.s.bundleN++
	if req.Msg.GetIfNoneMatch() != "" && req.Msg.GetIfNoneMatch() == p.s.Bundle.Version {
		p.s.notMod++
		return connect.NewResponse(&promptsv1.GetBundleResponse{NotModified: true}), nil
	}
	b := *p.s.Bundle
	b.Model = req.Msg.GetModel()
	return connect.NewResponse(&promptsv1.GetBundleResponse{Bundle: prompts.ToProto(&b)}), nil
}

// ---- DeviceAuthService ----

type authHandler struct {
	authv1connect.UnimplementedDeviceAuthServiceHandler
	s *Server
}

func (a authHandler) StartDeviceAuth(ctx context.Context, req *connect.Request[authv1.StartDeviceAuthRequest]) (*connect.Response[authv1.StartDeviceAuthResponse], error) {
	return connect.NewResponse(&authv1.StartDeviceAuthResponse{DeviceCode: "dc_test", UserCode: "TEST-CODE",
		VerificationUri: "http://fake/device", VerificationUriComplete: "http://fake/device?code=TEST-CODE", ExpiresInSeconds: 60, IntervalSeconds: 1}), nil
}

func (a authHandler) PollDeviceAuth(ctx context.Context, req *connect.Request[authv1.PollDeviceAuthRequest]) (*connect.Response[authv1.PollDeviceAuthResponse], error) {
	if req.Msg.GetDeviceCode() != "dc_test" {
		return connect.NewResponse(&authv1.PollDeviceAuthResponse{Status: authv1.PollDeviceAuthResponse_STATUS_EXPIRED}), nil
	}
	a.s.mu.Lock()
	a.s.polls++
	first := a.s.polls == 1
	a.s.mu.Unlock()
	if first {
		return connect.NewResponse(&authv1.PollDeviceAuthResponse{Status: authv1.PollDeviceAuthResponse_STATUS_PENDING}), nil
	}
	return connect.NewResponse(&authv1.PollDeviceAuthResponse{Status: authv1.PollDeviceAuthResponse_STATUS_APPROVED, Credentials: &authv1.DeviceCredentials{
		DeviceId: "dev_test", DeviceToken: "hdt_test", AccessToken: a.s.AccessToken, AccessExpiresInSeconds: 3600,
		User: &authv1.User{Id: "usr_test", Email: "test@example.com", Name: "Test User", Role: authv1.User_ROLE_ADMIN},
	}}), nil
}

func (a authHandler) RefreshAccessToken(ctx context.Context, req *connect.Request[authv1.RefreshAccessTokenRequest]) (*connect.Response[authv1.RefreshAccessTokenResponse], error) {
	if req.Msg.GetDeviceToken() != "hdt_test" {
		return nil, rpc.NewError(connect.CodeUnauthenticated, commonv1.ErrorType_ERROR_TYPE_UNAUTHENTICATED, "device revoked", 0)
	}
	a.s.mu.Lock()
	a.s.refreshN++
	n := a.s.refreshN
	a.s.mu.Unlock()
	return connect.NewResponse(&authv1.RefreshAccessTokenResponse{AccessToken: fmt.Sprintf("%s_%d", a.s.AccessToken, n), AccessExpiresInSeconds: 3600}), nil
}

func (a authHandler) GetMe(ctx context.Context, req *connect.Request[authv1.GetMeRequest]) (*connect.Response[authv1.GetMeResponse], error) {
	if err := a.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.GetMeResponse{User: &authv1.User{Id: "usr_test", Email: "test@example.com", Name: "Test User", Role: authv1.User_ROLE_ADMIN},
		Device: &authv1.Device{Id: "dev_test", Name: "test-device"}}), nil
}

func (a authHandler) Logout(ctx context.Context, req *connect.Request[authv1.LogoutRequest]) (*connect.Response[authv1.LogoutResponse], error) {
	if err := a.s.checkAuth(req.Header()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&authv1.LogoutResponse{}), nil
}

// ---- builders ----

// BlockSpec describes one content block of a scripted message.
type BlockSpec struct {
	kind      string
	text      string
	signature string
	id, name  string
	input     string
}

// Text is a text block.
func Text(s string) BlockSpec { return BlockSpec{kind: "text", text: s} }

// Thinking is a thinking block with a signature.
func Thinking(s, sig string) BlockSpec { return BlockSpec{kind: "thinking", text: s, signature: sig} }

// ToolUse is a tool_use block; input is a JSON object.
func ToolUse(id, name string, input any) BlockSpec {
	var s string
	switch v := input.(type) {
	case string:
		s = v
	default:
		b, _ := json.Marshal(v)
		s = string(b)
	}
	return BlockSpec{kind: "tool_use", id: id, name: name, input: s}
}

func chunks(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// Message builds a complete event sequence for one assistant message.
func Message(id string, blocks ...BlockSpec) []*llmv1.StreamMessageResponse {
	return Partial(id, true, blocks...)
}

// Partial builds events; when complete is false the message_delta/stop
// events are omitted (use with Reply.Hold to simulate a stalled stream).
func Partial(id string, complete bool, blocks ...BlockSpec) []*llmv1.StreamMessageResponse {
	ev := []*llmv1.StreamMessageResponse{{Event: &llmv1.StreamMessageResponse_MessageStart{MessageStart: &llmv1.MessageStart{Id: id, Model: "fake-model", Usage: &llmv1.Usage{InputTokens: 100}}}}}
	stop := llmv1.StopReason_STOP_REASON_END_TURN
	for i, b := range blocks {
		idx := int32(i)
		var start *llmv1.ContentBlock
		var deltas []*llmv1.ContentBlockDelta
		switch b.kind {
		case "text":
			start = &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Text{Text: &llmv1.TextBlock{}}}
			for _, c := range chunks(b.text, 7) {
				deltas = append(deltas, &llmv1.ContentBlockDelta{Index: idx, Delta: &llmv1.ContentBlockDelta_Text{Text: c}})
			}
		case "thinking":
			start = &llmv1.ContentBlock{Block: &llmv1.ContentBlock_Thinking{Thinking: &llmv1.ThinkingBlock{}}}
			deltas = append(deltas, &llmv1.ContentBlockDelta{Index: idx, Delta: &llmv1.ContentBlockDelta_Thinking{Thinking: b.text}},
				&llmv1.ContentBlockDelta{Index: idx, Delta: &llmv1.ContentBlockDelta_Signature{Signature: b.signature}})
		case "tool_use":
			stop = llmv1.StopReason_STOP_REASON_TOOL_USE
			start = &llmv1.ContentBlock{Block: &llmv1.ContentBlock_ToolUse{ToolUse: &llmv1.ToolUseBlock{Id: b.id, Name: b.name}}}
			for _, c := range chunks(b.input, 11) {
				deltas = append(deltas, &llmv1.ContentBlockDelta{Index: idx, Delta: &llmv1.ContentBlockDelta_InputJson{InputJson: c}})
			}
		}
		ev = append(ev, &llmv1.StreamMessageResponse{Event: &llmv1.StreamMessageResponse_ContentBlockStart{ContentBlockStart: &llmv1.ContentBlockStart{Index: idx, Block: start}}})
		if i == 0 {
			ev = append(ev, &llmv1.StreamMessageResponse{Event: &llmv1.StreamMessageResponse_Ping{Ping: &llmv1.Ping{}}})
		}
		for _, d := range deltas {
			ev = append(ev, &llmv1.StreamMessageResponse{Event: &llmv1.StreamMessageResponse_ContentBlockDelta{ContentBlockDelta: d}})
		}
		ev = append(ev, &llmv1.StreamMessageResponse{Event: &llmv1.StreamMessageResponse_ContentBlockStop{ContentBlockStop: &llmv1.ContentBlockStop{Index: idx}}})
	}
	if !complete {
		return ev
	}
	ev = append(ev,
		&llmv1.StreamMessageResponse{Event: &llmv1.StreamMessageResponse_MessageDelta{MessageDelta: &llmv1.MessageDelta{StopReason: stop, Usage: &llmv1.Usage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 50}}}},
		&llmv1.StreamMessageResponse{Event: &llmv1.StreamMessageResponse_MessageStop{MessageStop: &llmv1.MessageStop{CostUsd: 0.001, UpstreamModel: "fake-upstream"}}},
	)
	return ev
}

// WithInputTokens sets the reported input tokens of a scripted message
// (message_start and message_delta usage; cache reads are cleared).
func WithInputTokens(events []*llmv1.StreamMessageResponse, n int) []*llmv1.StreamMessageResponse {
	for _, ev := range events {
		switch e := ev.Event.(type) {
		case *llmv1.StreamMessageResponse_MessageStart:
			if e.MessageStart.Usage != nil {
				e.MessageStart.Usage.InputTokens = int64(n)
			}
		case *llmv1.StreamMessageResponse_MessageDelta:
			if e.MessageDelta.Usage != nil {
				e.MessageDelta.Usage.InputTokens = int64(n)
				e.MessageDelta.Usage.CacheReadInputTokens = 0
			}
		}
	}
	return events
}

// ErrOverloaded is a retryable gateway error.
var ErrOverloaded = rpc.NewError(connect.CodeUnavailable, commonv1.ErrorType_ERROR_TYPE_OVERLOADED, "overloaded", 10)

var cwdRe = regexp.MustCompile(`Working directory: (.+)`)

// Cwd extracts the working directory from the rendered env system section.
func Cwd(req *llm.Request) string {
	for _, s := range req.System {
		if m := cwdRe.FindStringSubmatch(s.Text); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// LastToolResults returns the tool_result blocks of the last user message.
func LastToolResults(req *llm.Request) []llm.ContentBlock {
	if len(req.Messages) == 0 {
		return nil
	}
	var out []llm.ContentBlock
	for _, b := range req.Messages[len(req.Messages)-1].Content {
		if b.Type == llm.BlockToolResult {
			out = append(out, b)
		}
	}
	return out
}

// ResultText concatenates the text of a tool_result block.
func ResultText(b llm.ContentBlock) string {
	var sb strings.Builder
	for _, c := range b.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// Sequence returns a responder that plays replies in order; extra requests
// get a final "done" message.
func Sequence(replies ...func(req *llm.Request) Reply) Responder {
	return func(n int, req *llm.Request) Reply {
		if n < len(replies) {
			return replies[n](req)
		}
		return Reply{Events: Message(fmt.Sprintf("msg_%d", n), Text("done"))}
	}
}

// ErrNoCwd is returned by demo scripts when the request has no env section.
var ErrNoCwd = errors.New("fake gateway: no working directory in system prompt")
