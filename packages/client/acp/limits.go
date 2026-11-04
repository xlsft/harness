package acp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/xlsft/harness/packages/client/acp/registry"
)

// LimitsSource is what harness knows about one agent's subscription limits, beyond the generic _meta parsing every
// agent gets (registry.ParseLimits). A new source is one file and one line in limitsSources. Limits stay on this
// machine like everything about ACP agents: not sent to the harness server, not shown to admins.
type LimitsSource interface {
	// Event reads a report of this agent's own from its _meta or extension params and returns the agent's limits as
	// they stand after it, on top of old. nil when v holds none (the generic parsing still looks at it).
	Event(v any, old *registry.Limits, now time.Time) *registry.Limits
	// Fetch asks the agent for its limits in a hidden session of its own, never a user's, and only if mayAsk says so
	// just before it asks. nil without an error: nothing to report, the last numbers stand. ErrLimitsCostTokens: do
	// not ask this agent again.
	Fetch(ctx context.Context, cfg Config, mayAsk func() bool) (*registry.Limits, error)
}

// ErrLimitsCostTokens says that asking for the limits reached the model, which it must never do.
var ErrLimitsCostTokens = errors.New("asking the agent for its limits spent tokens")

// limitsSources holds the sources by ACP registry id.
var limitsSources = map[string]LimitsSource{"claude-acp": claudeLimits{}}

// LimitsSourceFor returns the source of an agent; nil for one that only has the generic parsing.
func LimitsSourceFor(agentID string) LimitsSource { return limitsSources[agentID] }

// hiddenPrompt is one prompt in a short-lived session that no transcript ever shows.
type hiddenPrompt struct {
	// command must be among the commands the session advertises; text is sent only then.
	command, text string
	// meta is the _meta of session/new.
	meta map[string]any
	// mayAsk is asked right before the prompt goes out (nil: yes).
	mayAsk func() bool
	// cancelOnReply cancels the turn as soon as reply text arrives: nothing after the reply is wanted.
	cancelOnReply bool
}

// hiddenReply is what came back; asked is false when no prompt was sent.
type hiddenReply struct {
	asked  bool
	text   string
	tokens int
}

const (
	// hiddenCommandsWait bounds the wait for the session's commands, which arrive after session/new.
	hiddenCommandsWait = 15 * time.Second
	// hiddenReplySettle bounds the wait for the rest of a reply once its text or its end is in: the two are handled
	// independently here, in either order.
	hiddenReplySettle = 3 * time.Second
)

// askHidden starts the agent in cfg.Cwd, creates a session, sends the prompt if the agent advertises the command,
// collects the reply and closes everything. Nothing is published and nothing is stored.
func askHidden(ctx context.Context, cfg Config, h hiddenPrompt) (hiddenReply, error) {
	cfg.ResumeAgentSessionID, cfg.InitialModel, cfg.InitialMode, cfg.InitialEffort = "", "", "", ""
	cfg.Sink, cfg.OnLimits, cfg.OnAgentSession, cfg.OnAuth, cfg.LimitsSource = nil, nil, nil, nil, nil
	cfg.InitialLimits, cfg.SessionMeta = nil, h.meta
	s := New(cfg)
	defer s.Close()
	if err := s.Open(ctx); err != nil {
		return hiddenReply{}, err
	}
	defer s.closeAgentSession(ctx)
	ok, err := s.waitForCommand(ctx, h.command)
	if err != nil || !ok || (h.mayAsk != nil && !h.mayAsk()) {
		return hiddenReply{}, err
	}
	return s.promptHidden(ctx, h)
}

// waitForCommand waits for the session's command list and says whether it has the command. Without a list there is
// no answer, which counts as no.
func (s *Session) waitForCommand(ctx context.Context, name string) (bool, error) {
	wait := time.NewTimer(hiddenCommandsWait)
	defer wait.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		listed, found := len(s.commands) > 0, false
		for _, c := range s.commands {
			found = found || c.Name == name
		}
		s.mu.Unlock()
		if listed {
			return found, nil
		}
		select {
		case <-tick.C:
		case <-wait.C:
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (s *Session) promptHidden(ctx context.Context, h hiddenPrompt) (hiddenReply, error) {
	var mu sync.Mutex
	var text strings.Builder
	var once sync.Once
	replied := make(chan struct{})
	s.mu.Lock()
	p, id := s.proc, s.sessionID
	s.hidden = func(t string) {
		mu.Lock()
		text.WriteString(t)
		mu.Unlock()
		once.Do(func() { close(replied) })
	}
	s.mu.Unlock()
	if p == nil {
		return hiddenReply{}, ErrNotReady
	}
	type outcome struct {
		res sdk.PromptResponse
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := p.conn.Prompt(ctx, sdk.PromptRequest{SessionId: id, Prompt: []sdk.ContentBlock{sdk.TextBlock(h.text)}})
		done <- outcome{res, err}
	}()
	var end *outcome
	select {
	case <-replied:
		if h.cancelOnReply {
			if err := p.conn.Cancel(ctx, sdk.CancelNotification{SessionId: id}); err != nil {
				s.log.Debug("acp hidden prompt: session/cancel failed", "agent", s.cfg.AgentID, "err", err)
			}
		}
	case o := <-done:
		end = &o
	case <-ctx.Done():
		return hiddenReply{asked: true}, ctx.Err()
	}
	settle := time.NewTimer(hiddenReplySettle)
	defer settle.Stop()
	if end == nil {
		select {
		case o := <-done:
			end = &o
		case <-settle.C:
		case <-ctx.Done():
		}
	} else {
		select {
		case <-replied:
		case <-settle.C:
		case <-ctx.Done():
		}
	}
	out := hiddenReply{asked: true}
	mu.Lock()
	out.text = text.String()
	mu.Unlock()
	if end != nil {
		if end.err != nil && out.text == "" {
			return out, end.err
		}
		if u := end.res.Usage; u != nil {
			out.tokens = u.TotalTokens
		}
	}
	return out, nil
}
