package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/keepawake"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

// The session's side of the tools that change the session and the user's preferences (tools.SessionControl), and what
// belongs to this machine: the keep-awake switch and the device settings. Nothing here reaches the harness server: a
// device choice stays in HARNESS_HOME, unlike the synced user settings (preferences.go), which keep their own keys.

// modelUsageCheckTimeout bounds the look at the limits of the provider a tool wants to move the session to.
const modelUsageCheckTimeout = 5 * time.Second

// ---- Where the user is ----

type relayOriginKey struct{}

// WithRelayOrigin marks a command as forwarded by the relay: the user sent it from another machine.
func WithRelayOrigin(ctx context.Context) context.Context {
	return context.WithValue(ctx, relayOriginKey{}, true)
}

func isRelayOrigin(ctx context.Context) bool {
	on, _ := ctx.Value(relayOriginKey{}).(bool)
	return on
}

// ElsewhereReason implements tools.DeviceOwner.
func (ls *liveSession) ElsewhereReason() string {
	if !ls.drivenByRelay.Load() {
		return ""
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "its own machine"
	}
	return "this session runs on " + host + " and the user is driving it from another machine, so it would open where they are not; tell them the path instead"
}

// ---- Keep awake ----

// keepAwake is the one switch of this client's sessions.
func (s *Server) keepAwake() *keepawake.Holder {
	s.awakeOnce.Do(func() {
		s.awake = keepawake.New(s.Inhibitor)
		s.awake.OnError = func(err error) { s.log().Warn("keep awake", "err", err) }
	})
	return s.awake
}

func statusBusy(st agentv1.SessionStatus) bool {
	return st == agentv1.SessionStatus_SESSION_STATUS_WORKING || st == agentv1.SessionStatus_SESSION_STATUS_NEEDS_INPUT
}

// SetKeepAwake turns the session's "Keep computer awake" switch on or off.
func (s *Server) SetKeepAwake(ctx context.Context, req *connect.Request[agentv1.SetKeepAwakeRequest]) (*connect.Response[agentv1.SetKeepAwakeResponse], error) {
	id, on := req.Msg.GetSessionId(), req.Msg.GetEnabled()
	if l := s.acpLive(id); l != nil {
		if err := s.keepAwake().Set(id, on); err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		l.publishInfo()
		return connect.NewResponse(&agentv1.SetKeepAwakeResponse{}), nil
	}
	ls, err := s.get(id)
	if err != nil {
		if s.remoteInfo(id) != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session runs on another machine; keeping that machine awake is decided there"))
		}
		return nil, err
	}
	if err := ls.SetKeepAwake(on); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentv1.SetKeepAwakeResponse{}), nil
}

// SetKeepAwake implements tools.SessionControl.
func (ls *liveSession) SetKeepAwake(on bool) error {
	if err := ls.srv.keepAwake().Set(ls.sess.ID, on); err != nil {
		return err
	}
	ls.publishInfo()
	return nil
}

// ---- Device settings ----

const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

type deviceSettingsFile struct {
	Theme         string `json:"theme,omitempty"`
	Notifications *bool  `json:"notifications,omitempty"`
}

func deviceSettingsFilePath() string { return filepath.Join(config.Home(), "device-settings.json") }

func readDeviceSettings() deviceSettingsFile {
	var f deviceSettingsFile
	if b, err := os.ReadFile(deviceSettingsFilePath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

func deviceSettingsProto(f deviceSettingsFile) *agentv1.DeviceSettings {
	return &agentv1.DeviceSettings{Theme: f.Theme, Notifications: f.Notifications == nil || *f.Notifications}
}

// updateDeviceSettings changes the given fields and tells the frontends watching this client's sessions.
func (s *Server) updateDeviceSettings(theme *string, notifications *bool) (deviceSettingsFile, error) {
	s.deviceMu.Lock()
	f := readDeviceSettings()
	if theme != nil {
		if t := *theme; t != themeSystem && t != themeLight && t != themeDark {
			s.deviceMu.Unlock()
			return f, fmt.Errorf("unknown theme %q", t)
		}
		f.Theme = *theme
	}
	if notifications != nil {
		f.Notifications = notifications
	}
	err := writeDeviceSettings(f)
	s.deviceMu.Unlock()
	if err != nil {
		return f, err
	}
	s.mu.Lock()
	live := make([]*liveSession, 0, len(s.live))
	for _, ls := range s.live {
		live = append(live, ls)
	}
	s.mu.Unlock()
	for _, ls := range live {
		ls.hub.publishEphemeral(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_DeviceSettingsChanged{DeviceSettingsChanged: &agentv1.DeviceSettingsChanged{}}})
	}
	return f, nil
}

func writeDeviceSettings(f deviceSettingsFile) error {
	p := deviceSettingsFilePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Server) GetDeviceSettings(context.Context, *connect.Request[agentv1.GetDeviceSettingsRequest]) (*connect.Response[agentv1.GetDeviceSettingsResponse], error) {
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	return connect.NewResponse(&agentv1.GetDeviceSettingsResponse{Settings: deviceSettingsProto(readDeviceSettings())}), nil
}

func (s *Server) UpdateDeviceSettings(_ context.Context, req *connect.Request[agentv1.UpdateDeviceSettingsRequest]) (*connect.Response[agentv1.UpdateDeviceSettingsResponse], error) {
	f, err := s.updateDeviceSettings(req.Msg.Theme, req.Msg.Notifications)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentv1.UpdateDeviceSettingsResponse{Settings: deviceSettingsProto(f)}), nil
}

// ---- tools.SessionControl ----

// plainError drops the Connect code from what a tool shows the model.
func plainError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

func (ls *liveSession) SessionModel() string { return ls.sess.Agent.Model() }

// SwitchModel goes further than the SetModel RPC, where the user makes the choice: the model may not leave the policy's
// list or move to a provider that is out of usage (the user can still choose one; messages are then held).
func (ls *liveSession) SwitchModel(ctx context.Context, model string) (string, error) {
	if st := ls.sess.Settings; st != nil && len(st.AvailableModels) > 0 && !slices.Contains(st.AvailableModels, model) {
		return "", fmt.Errorf("your administrator allows only %s", strings.Join(st.AvailableModels, ", "))
	}
	if m := ls.usage.monitor; m != nil {
		cctx, cancel := context.WithTimeout(ctx, modelUsageCheckTimeout)
		r := m.Recheck(cctx, model)
		cancel()
		if r.Known && r.Status.State == usagelimit.Reached {
			name := r.Status.ProviderName
			if name == "" {
				name = "its provider"
			}
			return "", fmt.Errorf("%s is out of usage; ask the user before choosing another model", name)
		}
	}
	display, err := ls.srv.switchModel(ctx, ls, model)
	return display, plainError(err)
}

func (ls *liveSession) SessionEffort() string { return ls.sess.Agent.Effort() }

// SwitchEffort implements tools.SessionControl: the level must be one the session's model offers, when it says.
func (ls *liveSession) SwitchEffort(effort string) error {
	if pbconv.Effort(effort) == 0 {
		return fmt.Errorf("InputValidationError: effort is one of low, medium, high, xhigh, max; got %q", effort)
	}
	if ls.sess.Catalog != nil {
		if m, ok := ls.sess.Catalog.Find(ls.sess.Agent.Model()); ok && len(m.Capabilities.Effort) > 0 && !slices.Contains(m.Capabilities.Effort, effort) {
			return fmt.Errorf("%s offers the effort levels %s", m.DisplayName, strings.Join(m.Capabilities.Effort, ", "))
		}
	}
	ls.sess.Agent.SetEffort(effort)
	ls.publishInfo()
	return nil
}

func (ls *liveSession) SessionTitle() string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.title
}

// RenameSession implements tools.SessionControl through UpdateSession, the frontends' rename.
func (ls *liveSession) RenameSession(ctx context.Context, title string) (string, error) {
	res, err := ls.srv.UpdateSession(ctx, connect.NewRequest(&agentv1.UpdateSessionRequest{SessionId: ls.sess.ID, Title: &title}))
	if err != nil {
		return "", plainError(err)
	}
	return res.Msg.GetSession().GetTitle(), nil
}

// NoteChange implements tools.SessionControl.
func (ls *liveSession) NoteChange(kind, text string) {
	ls.notice(agentv1.Notice_LEVEL_INFO, kind, text)
}

// Setting implements tools.SessionControl. Only the keys of tools.SettingDefs have a case: anything else has no way in.
func (ls *liveSession) Setting(ctx context.Context, key string) (string, error) {
	switch key {
	case tools.SettingTheme:
		if t := readDeviceSettings().Theme; t != "" {
			return t, nil
		}
		return themeSystem, nil
	case tools.SettingNotifications:
		return strconv.FormatBool(deviceSettingsProto(readDeviceSettings()).GetNotifications()), nil
	case tools.SettingOutputStyle:
		return ls.sess.ActiveOutputStyle(), nil
	case tools.SettingKeepAwake:
		return strconv.FormatBool(ls.srv.keepAwake().Wanted(ls.sess.ID)), nil
	case tools.SettingCompactIdleSessions:
		return ls.syncedBool(ctx, settings.KeyCompactIdleSessions, ls.sess.Settings != nil && ls.sess.Settings.CompactIdleSessions)
	case tools.SettingInterruptReply:
		return ls.syncedBool(ctx, settings.KeyInterruptReply, ls.sess.Settings != nil && ls.sess.Settings.InterruptReply)
	}
	return "", fmt.Errorf("unknown setting %q", key)
}

// syncedBool reads a key of the user's settings document; without a server, what the session loaded.
func (ls *liveSession) syncedBool(ctx context.Context, key string, loaded bool) (string, error) {
	s := ls.srv
	if s.Preferences == nil {
		return strconv.FormatBool(loaded), nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.userSettings(ctx)
	if err != nil {
		return "", plainError(syncError(err))
	}
	var raw map[string]json.RawMessage
	v := true // the default of both keys
	if json.Unmarshal([]byte(doc.GetContent()), &raw) == nil {
		if r, ok := raw[key]; ok {
			_ = json.Unmarshal(r, &v)
		}
	}
	return strconv.FormatBool(v), nil
}

// ApplySetting implements tools.SessionControl.
func (ls *liveSession) ApplySetting(ctx context.Context, key, value string) error {
	s := ls.srv
	on := value == "true"
	switch key {
	case tools.SettingTheme:
		_, err := s.updateDeviceSettings(&value, nil)
		return err
	case tools.SettingNotifications:
		_, err := s.updateDeviceSettings(nil, &on)
		return err
	case tools.SettingOutputStyle:
		return ls.applyOutputStyle(value)
	case tools.SettingKeepAwake:
		return ls.SetKeepAwake(on)
	case tools.SettingCompactIdleSessions, tools.SettingInterruptReply:
		if s.Preferences == nil {
			return errors.New("this preference is stored on the harness server, and this client is not connected to one")
		}
		ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
		defer cancel()
		name := settings.KeyCompactIdleSessions
		if key == tools.SettingInterruptReply {
			name = settings.KeyInterruptReply
		}
		_, err := s.mergeUserSettings(ctx, map[string]any{name: on})
		return plainError(err)
	}
	return fmt.Errorf("unknown setting %q", key)
}
