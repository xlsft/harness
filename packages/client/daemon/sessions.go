package daemon

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/server"
)

// ServerSessions is Sessions over this process's agent server.
type ServerSessions struct{ S *server.Server }

// Create starts a session, or resumes or forks one, and returns its id.
func (a ServerSessions) Create(ctx context.Context, req *agentv1.CreateSessionRequest) (string, error) {
	res, err := a.S.CreateSession(ctx, connect.NewRequest(req))
	if err != nil {
		return "", err
	}
	return res.Msg.GetSession().GetId(), nil
}

// SetTitle names a session.
func (a ServerSessions) SetTitle(ctx context.Context, id, title string) error {
	_, err := a.S.UpdateSession(ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: id, Title: &title}))
	return err
}

// Send is the user's first message. It comes from the person who started the work.
func (a ServerSessions) Send(ctx context.Context, id, text string) error {
	_, err := a.S.SendUserMessage(ctx, connect.NewRequest(&agentv1.SendUserMessageRequest{
		SessionId: id, Uuid: uuid.NewString(), Content: pbconv.Blocks([]llm.ContentBlock{llm.TextBlock(text)}),
	}))
	return err
}

// DeliverJob gives a session an auto-fix job, as a reminder and never as a user message.
func (a ServerSessions) DeliverJob(ctx context.Context, id string, job *agentv1.AutoFixJob) error {
	return a.S.DeliverAutoFixJob(ctx, &agentv1.DeliverAutoFixJobRequest{SessionId: id, Job: job})
}

// Close closes a session; the transcript stays.
func (a ServerSessions) Close(ctx context.Context, id string) error {
	_, err := a.S.CloseSession(ctx, connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: id}))
	return err
}

// Live lists the open sessions.
func (a ServerSessions) Live() []string { return a.S.LiveSessionIDs() }

// Status says how an open session is doing.
func (a ServerSessions) Status(id string) (agentv1.SessionStatus, time.Time, bool) {
	f, ok := a.S.SessionFeed(id)
	if !ok {
		return 0, time.Time{}, false
	}
	info := f.Info()
	updated, _ := time.Parse(time.RFC3339, info.GetUpdatedAt())
	return info.GetStatus(), updated, true
}

// Events follows a session. after < 0 starts where this process opened it, past any history replayed from its transcript.
func (a ServerSessions) Events(id string, after int64) (<-chan *agentv1.SessionEvent, int64, func(), bool) {
	f, ok := a.S.SessionFeed(id)
	if !ok {
		return nil, 0, nil, false
	}
	if after < 0 {
		after = f.HistoryLen()
	}
	stored, live, cancel := f.Subscribe(after)
	ch := make(chan *agentv1.SessionEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(ch)
		for _, ev := range stored {
			select {
			case ch <- ev:
			case <-done:
				return
			}
		}
		for {
			select {
			case ev, ok := <-live:
				if !ok {
					return
				}
				select {
				case ch <- ev:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()
	return ch, after, func() {
		close(done)
		cancel()
	}, true
}

// Publish tells the session's viewers what became of its work.
func (a ServerSessions) Publish(id string, update *agentv1.HostWorkUpdated) {
	a.S.PublishHostWork(id, update)
}
