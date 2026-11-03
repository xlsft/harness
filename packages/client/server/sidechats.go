package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	llmv1 "github.com/xlsft/harness/packages/client/gen/harness/llm/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/sidechat"
)

// Side chats (sidechat.proto): read-only conversations next to an open session of this machine. They are
// stored next to the session's transcript (sidechat.DirFor) and answered through the session's gateway with the
// side chat prompt, a rendering of the main conversation and no tools (sidechat.Request). Sessions of other
// machines are refused for now: their transcript lives there, and the server's synced copy is not read here.

const maxSideChatQuestion = 32_000

// sideChatRuns are the running answers, by side chat id: their cancel functions.
type sideChatRuns struct {
	mu   sync.Mutex
	runs map[string]context.CancelFunc
}

// start registers a running answer; false when one already runs for id.
func (r *sideChatRuns) start(id string, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]context.CancelFunc{}
	}
	if _, ok := r.runs[id]; ok {
		return false
	}
	r.runs[id] = cancel
	return true
}

func (r *sideChatRuns) finish(id string) {
	r.mu.Lock()
	delete(r.runs, id)
	r.mu.Unlock()
}

// stop cancels the running answer of id; false when none runs.
func (r *sideChatRuns) stop(id string) bool {
	r.mu.Lock()
	cancel, ok := r.runs[id]
	r.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (r *sideChatRuns) running(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.runs[id]
	return ok
}

// sideChatSession returns the open local session and the store of its side chats.
func (s *Server) sideChatSession(id string) (*liveSession, sidechat.Store, error) {
	s.mu.Lock()
	ls := s.live[id]
	s.mu.Unlock()
	if ls == nil {
		if remote := s.remoteInfo(id); remote != nil {
			return nil, sidechat.Store{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s runs on %s; side chats are only available for sessions of this machine", id, deviceLabel(remote)))
		}
		return nil, sidechat.Store{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s is not open; call CreateSession first", id))
	}
	return ls, sidechat.Store{Dir: sidechat.DirFor(ls.sess.Transcript.Path())}, nil
}

func sideChatError(err error) error {
	if errors.Is(err, sidechat.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeInternal, err)
}

// sideChatModel validates a model id against the session's catalog (any id without a catalog).
func sideChatModel(ls *liveSession, id string) (llm.Model, error) {
	if ls.sess.Catalog == nil {
		return llm.Model{ID: id}, nil
	}
	m, ok := ls.sess.Catalog.Find(id)
	if !ok {
		return llm.Model{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown model %s", id))
	}
	return m, nil
}

func (s *Server) sideChatInfo(c *sidechat.Chat) *agentv1.SideChatInfo {
	return &agentv1.SideChatInfo{
		SideChatId: c.ID, SessionId: c.SessionID, Title: c.Title, Model: c.Model, Effort: pbconv.Effort(c.Effort),
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		MessageCount: uint32(len(c.Messages)), Answering: s.sides.running(c.ID),
	}
}

func sideChatMessage(m sidechat.Message) *agentv1.SideChatMessage {
	pm := &agentv1.SideChatMessage{
		MessageId: m.ID, Role: m.Role, Text: m.Text, Quote: m.Quote, Model: m.Model, Effort: pbconv.Effort(m.Effort),
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339Nano), Stopped: m.Stopped, Error: m.Error,
	}
	if m.Usage != nil {
		pm.Usage = pbconv.Usage(*m.Usage)
	}
	return pm
}

func (s *Server) ListSideChats(ctx context.Context, req *connect.Request[agentv1.ListSideChatsRequest]) (*connect.Response[agentv1.ListSideChatsResponse], error) {
	_, store, err := s.sideChatSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	chats, err := store.List()
	if err != nil {
		return nil, sideChatError(err)
	}
	resp := &agentv1.ListSideChatsResponse{}
	for _, c := range chats {
		resp.SideChats = append(resp.SideChats, s.sideChatInfo(c))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) CreateSideChat(ctx context.Context, req *connect.Request[agentv1.CreateSideChatRequest]) (*connect.Response[agentv1.CreateSideChatResponse], error) {
	m := req.Msg
	ls, store, err := s.sideChatSession(m.GetSessionId())
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(m.GetModel())
	if model == "" {
		model = ls.sess.Agent.Model()
	}
	if _, err := sideChatModel(ls, model); err != nil {
		return nil, err
	}
	effort := pbconv.FromEffort(m.GetEffort())
	if m.GetEffort() == llmv1.Effort_EFFORT_UNSPECIFIED {
		effort = ls.sess.Agent.Effort()
	}
	title, titleSet := strings.TrimSpace(m.GetTitle()), true
	if title == "" {
		title, titleSet = sidechat.DefaultTitle, false
	}
	now := time.Now().UTC()
	c := &sidechat.Chat{ID: sidechat.NewID("sc_"), SessionID: m.GetSessionId(), Title: title, TitleSet: titleSet, Model: model, Effort: effort, CreatedAt: now, UpdatedAt: now, Messages: []sidechat.Message{}}
	if err := store.Save(c); err != nil {
		return nil, sideChatError(err)
	}
	return connect.NewResponse(&agentv1.CreateSideChatResponse{SideChat: s.sideChatInfo(c)}), nil
}

func (s *Server) GetSideChat(ctx context.Context, req *connect.Request[agentv1.GetSideChatRequest]) (*connect.Response[agentv1.GetSideChatResponse], error) {
	_, store, err := s.sideChatSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	c, err := store.Get(req.Msg.GetSideChatId())
	if err != nil {
		return nil, sideChatError(err)
	}
	resp := &agentv1.GetSideChatResponse{SideChat: s.sideChatInfo(c)}
	for _, m := range c.Messages {
		resp.Messages = append(resp.Messages, sideChatMessage(m))
	}
	return connect.NewResponse(resp), nil
}

func (s *Server) UpdateSideChat(ctx context.Context, req *connect.Request[agentv1.UpdateSideChatRequest]) (*connect.Response[agentv1.UpdateSideChatResponse], error) {
	m := req.Msg
	ls, store, err := s.sideChatSession(m.GetSessionId())
	if err != nil {
		return nil, err
	}
	if m.Model != nil {
		if _, err := sideChatModel(ls, m.GetModel()); err != nil {
			return nil, err
		}
	}
	c, err := store.Update(m.GetSideChatId(), func(c *sidechat.Chat) error {
		if m.Title != nil {
			if t := strings.TrimSpace(m.GetTitle()); t != "" {
				c.Title, c.TitleSet = t, true
			}
		}
		if m.Model != nil {
			c.Model = m.GetModel()
		}
		if m.Effort != nil {
			c.Effort = pbconv.FromEffort(m.GetEffort())
		}
		return nil
	})
	if err != nil {
		return nil, sideChatError(err)
	}
	return connect.NewResponse(&agentv1.UpdateSideChatResponse{SideChat: s.sideChatInfo(c)}), nil
}

func (s *Server) DeleteSideChat(ctx context.Context, req *connect.Request[agentv1.DeleteSideChatRequest]) (*connect.Response[agentv1.DeleteSideChatResponse], error) {
	_, store, err := s.sideChatSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	s.sides.stop(req.Msg.GetSideChatId())
	if err := store.Delete(req.Msg.GetSideChatId()); err != nil {
		return nil, sideChatError(err)
	}
	return connect.NewResponse(&agentv1.DeleteSideChatResponse{}), nil
}

func (s *Server) StopSideChat(ctx context.Context, req *connect.Request[agentv1.StopSideChatRequest]) (*connect.Response[agentv1.StopSideChatResponse], error) {
	if _, _, err := s.sideChatSession(req.Msg.GetSessionId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.StopSideChatResponse{Stopped: s.sides.stop(req.Msg.GetSideChatId())}), nil
}

func (s *Server) AskSideChat(ctx context.Context, req *connect.Request[agentv1.AskSideChatRequest], stream *connect.ServerStream[agentv1.AskSideChatResponse]) error {
	m := req.Msg
	ls, store, err := s.sideChatSession(m.GetSessionId())
	if err != nil {
		return err
	}
	text := strings.TrimSpace(m.GetText())
	if text == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("text is required"))
	}
	if len(text) > maxSideChatQuestion || len(m.GetQuote()) > maxSideChatQuestion {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("text and quote must be at most %d bytes", maxSideChatQuestion))
	}
	if ls.sess.Gateway == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no model gateway is configured"))
	}
	id := m.GetSideChatId()
	if _, err := store.Get(id); err != nil {
		return sideChatError(err)
	}
	var model llm.Model
	if mid := strings.TrimSpace(m.GetModel()); mid != "" {
		if model, err = sideChatModel(ls, mid); err != nil {
			return err
		}
	}

	// The answer stops with StopSideChat, DeleteSideChat, a cancelled stream or the client shutting down.
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopOnShutdown := context.AfterFunc(s.ctx, cancel)
	defer stopOnShutdown()
	if !s.sides.start(id, cancel) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this side chat is already answering; stop it first"))
	}
	defer s.sides.finish(id)

	question := sidechat.Message{ID: sidechat.NewID("scm_"), Role: llm.RoleUser, Text: text, Quote: strings.TrimSpace(m.GetQuote()), CreatedAt: time.Now().UTC()}
	var history []sidechat.Message
	chat, err := store.Update(id, func(c *sidechat.Chat) error {
		if model.ID != "" {
			c.Model = model.ID
		}
		if m.GetEffort() != llmv1.Effort_EFFORT_UNSPECIFIED {
			c.Effort = pbconv.FromEffort(m.GetEffort())
		}
		if !c.TitleSet && len(c.Messages) == 0 {
			c.Title = sidechat.TitleFrom(text)
		}
		history = slices.Clone(c.Messages)
		c.Messages = append(c.Messages, question)
		return nil
	})
	if err != nil {
		return sideChatError(err)
	}
	if model.ID == "" {
		if model, err = sideChatModel(ls, chat.Model); err != nil {
			model = llm.Model{ID: chat.Model} // the catalog dropped it since: let the gateway decide
		}
	}
	if err := stream.Send(&agentv1.AskSideChatResponse{Event: &agentv1.AskSideChatResponse_Question{Question: sideChatMessage(question)}}); err != nil {
		cancel()
	}

	effort := chat.Effort
	if ls.sess.Catalog != nil && !slices.Contains(model.Capabilities.Effort, effort) {
		effort = ""
	}
	bundle := ls.sess.Agent.Bundle()
	version := ""
	if bundle != nil {
		version = bundle.Version
	}
	gwReq := sidechat.Request(sidechat.RequestOptions{
		SessionID: ls.sess.ID, Prompt: bundle.Reminder(sidechat.PromptKey, sidechat.FallbackPrompt), BundleVersion: version,
		Model: model.ID, ContextWindow: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens,
		Effort: effort, Thinking: model.Capabilities.Thinking,
		Main: ls.sess.Transcript.Entries(), History: history, Question: question.Text, Quote: question.Quote,
	})
	ans, askErr := sidechat.Ask(actx, ls.sess.Gateway, gwReq, func(delta string) {
		if err := stream.Send(&agentv1.AskSideChatResponse{Event: &agentv1.AskSideChatResponse_Delta{Delta: delta}}); err != nil {
			cancel()
		}
	})
	answer := sidechat.Message{ID: sidechat.NewID("scm_"), Role: llm.RoleAssistant, Text: ans.Text, Model: model.ID, Effort: effort, CreatedAt: time.Now().UTC(), Usage: ans.Usage}
	switch {
	case askErr == nil:
	case actx.Err() != nil:
		answer.Stopped = true
	default:
		answer.Error = askErr.Error()
		s.log().Warn("side chat answer failed", "session", ls.sess.ID, "side_chat", id, "err", askErr)
	}
	chat, err = store.Update(id, func(c *sidechat.Chat) error {
		c.Messages = append(c.Messages, answer)
		return nil
	})
	if errors.Is(err, sidechat.ErrNotFound) {
		return nil // deleted while answering
	}
	if err != nil {
		return sideChatError(err)
	}
	s.sides.finish(id) // the info below reports answering = false
	_ = stream.Send(&agentv1.AskSideChatResponse{Event: &agentv1.AskSideChatResponse_Answer{Answer: sideChatMessage(answer)}, SideChat: s.sideChatInfo(chat)})
	return nil
}
