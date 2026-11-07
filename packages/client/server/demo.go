package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/computeruse"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Recording a demo into a skill (demo_recording.proto, computeruse/recording.go). Like computer use, all of it is
// device-local: the RPCs refuse a frontend that is not on this machine and a session of another one, the events stay out
// of the relay mirror, and a recording is only started and stopped by the user. When one ends and is kept, the session's
// model gets the demo_recorded reminder and drafts the skill; DemoSkillSave's review card is answered here.

func demoState(st computeruse.RecordingState, profile computeruse.Profile) *agentv1.DemoRecordingState {
	out := &agentv1.DemoRecordingState{Recording: st.Recording, RecordingId: st.ID, Steps: int32(st.Steps), Hotkey: st.Hotkey, LastStep: st.Last}
	if !st.Started.IsZero() {
		out.StartedAt = st.Started.UTC().Format(time.RFC3339)
	}
	for _, a := range st.Apps {
		out.Apps = append(out.Apps, &agentv1.DemoRecordingApp{Id: a.ID, Name: a.Name, Tier: computerTiers[profile.TierFor(a)]})
	}
	return out
}

// onDemoRecording publishes a session's recording state.
func (s *Server) onDemoRecording(sessionID string, st computeruse.RecordingState) {
	ls := s.liveSession(sessionID)
	if ls == nil {
		return
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_DemoRecordingState{DemoRecordingState: demoState(st, s.computerUse().Profile())}})
}

// onDemoRecorded hands a kept recording to the session's model, waking the session if it is idle.
func (s *Server) onDemoRecorded(sessionID string, f computeruse.RecordingFile) {
	ls := s.liveSession(sessionID)
	if ls == nil || len(f.Steps) == 0 {
		return
	}
	var apps []string
	for _, a := range f.Apps {
		apps = append(apps, a.Name)
	}
	limited := map[string]string{}
	for _, st := range f.Steps {
		if st.AppID != "" && !st.Allowed {
			limited[st.AppName] = st.Tier
		}
	}
	var notes []string
	for app, tier := range limited {
		notes = append(notes, fmt.Sprintf("%s has the %s tier in the current security profile, so some of its steps cannot be done with computer use", app, tier))
	}
	ls.sess.Agent.Remind(agent.ReminderDemoRecorded, map[string]string{
		"recording_id": f.ID, "steps": strconv.Itoa(len(f.Steps)), "apps": strings.Join(apps, ", "), "notes": strings.Join(notes, ". "),
	})
	ls.driver.Wake()
}

// demoSession returns the live session a recording RPC is for, refusing one of another machine.
func (s *Server) demoSession(id string) (*liveSession, error) {
	ls, err := s.computerSession(id)
	if err != nil {
		return nil, err
	}
	return ls, nil
}

func refuseRemoteDemo(addr string) error {
	if localPeer(addr) {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("recording a demo can only be controlled from this machine"))
}

// ListDemoRecordingApps lists the apps a recording can watch.
func (s *Server) ListDemoRecordingApps(ctx context.Context, req *connect.Request[agentv1.ListDemoRecordingAppsRequest]) (*connect.Response[agentv1.ListDemoRecordingAppsResponse], error) {
	if err := refuseRemoteDemo(req.Peer().Addr); err != nil {
		return nil, err
	}
	m := s.computerUse()
	apps, err := m.RecordingApps(ctx)
	if err != nil {
		return connect.NewResponse(&agentv1.ListDemoRecordingAppsResponse{UnavailableReason: err.Error()}), nil
	}
	profile := m.Profile()
	out := &agentv1.ListDemoRecordingAppsResponse{}
	for _, a := range apps {
		out.Apps = append(out.Apps, &agentv1.DemoRecordingApp{Id: a.ID, Name: a.Name, Tier: computerTiers[a.Tier], TierNote: computeruse.TierNote(profile, a.App)})
	}
	return connect.NewResponse(out), nil
}

// StartDemoRecording starts recording what the user does in the apps picked.
func (s *Server) StartDemoRecording(ctx context.Context, req *connect.Request[agentv1.StartDemoRecordingRequest]) (*connect.Response[agentv1.StartDemoRecordingResponse], error) {
	if err := refuseRemoteDemo(req.Peer().Addr); err != nil {
		return nil, err
	}
	ls, err := s.demoSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	m := s.computerUse()
	st, err := m.StartRecording(ctx, ls.sess.ID, req.Msg.GetAppIds())
	switch {
	case errors.Is(err, computeruse.ErrRecordingBusy):
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.StartDemoRecordingResponse{State: demoState(st, m.Profile())}), nil
}

// StopDemoRecording stops the session's recording; kept, it goes to the agent.
func (s *Server) StopDemoRecording(_ context.Context, req *connect.Request[agentv1.StopDemoRecordingRequest]) (*connect.Response[agentv1.StopDemoRecordingResponse], error) {
	if err := refuseRemoteDemo(req.Peer().Addr); err != nil {
		return nil, err
	}
	ls, err := s.demoSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	f, err := s.computerUse().StopRecording(ls.sess.ID, req.Msg.GetDiscard())
	if errors.Is(err, computeruse.ErrNoRecording) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.StopDemoRecordingResponse{RecordingId: f.ID, Steps: int32(len(f.Steps))}), nil
}

type pendingSkillReview struct {
	ch    chan computeruse.SkillReviewAnswer
	scope string
}

// ReviewSkill implements computeruse.SkillReviewer: it publishes the review card and waits for the user. Stop cancels.
func (ls *liveSession) ReviewSkill(ctx context.Context, r computeruse.SkillReview) (computeruse.SkillReviewAnswer, error) {
	id := newUUID("sr_")
	p := &pendingSkillReview{ch: make(chan computeruse.SkillReviewAnswer, 1), scope: r.Scope}
	ls.mu.Lock()
	if ls.pendingReviews == nil {
		ls.pendingReviews = map[string]*pendingSkillReview{}
	}
	ls.pendingReviews[id] = p
	ls.mu.Unlock()
	defer func() {
		ls.mu.Lock()
		delete(ls.pendingReviews, id)
		ls.mu.Unlock()
	}()
	ls.beginWait()
	defer ls.endWait()
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_DemoSkillReviewRequested{DemoSkillReviewRequested: &agentv1.DemoSkillReviewRequested{
		RequestId: id, ToolUseId: r.ToolUseID, RecordingId: r.RecordingID, Name: r.Name, Scope: r.Scope, Content: r.Content,
		ProjectPath: r.PathFor[computeruse.SkillScopeProject], PersonalPath: r.PathFor[computeruse.SkillScopePersonal],
	}}})
	resolved := func(saved bool, scope, by string) {
		ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_DemoSkillReviewResolved{DemoSkillReviewResolved: &agentv1.DemoSkillReviewResolved{RequestId: id, Saved: saved, Scope: scope, ResolvedBy: by}}})
	}
	select {
	case <-ctx.Done():
		resolved(false, r.Scope, "interrupt")
		return computeruse.SkillReviewAnswer{}, ctx.Err()
	case a := <-p.ch:
		resolved(a.Saved, a.Scope, "local")
		return a, nil
	}
}

var _ computeruse.SkillReviewer = (*liveSession)(nil)

// RespondToDemoSkillReview is the user's answer to a review card.
func (s *Server) RespondToDemoSkillReview(_ context.Context, req *connect.Request[agentv1.RespondToDemoSkillReviewRequest]) (*connect.Response[agentv1.RespondToDemoSkillReviewResponse], error) {
	if err := refuseRemoteDemo(req.Peer().Addr); err != nil {
		return nil, err
	}
	ls, err := s.demoSession(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	m := req.Msg
	scope := m.GetScope()
	if scope != "" && scope != computeruse.SkillScopeProject && scope != computeruse.SkillScopePersonal {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scope is project or personal, not %q", scope))
	}
	if len(m.GetContent()) > 128<<10 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the skill is longer than 128 KiB"))
	}
	ls.mu.Lock()
	p := ls.pendingReviews[m.GetRequestId()]
	delete(ls.pendingReviews, m.GetRequestId()) // claimed: another frontend may have answered meanwhile
	ls.mu.Unlock()
	if p == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no pending skill review %s", m.GetRequestId()))
	}
	if scope == "" {
		scope = p.scope
	}
	p.ch <- computeruse.SkillReviewAnswer{Saved: m.GetSave(), Content: m.GetContent(), Scope: scope, Note: strings.TrimSpace(m.GetNote())}
	return connect.NewResponse(&agentv1.RespondToDemoSkillReviewResponse{}), nil
}
