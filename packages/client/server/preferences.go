package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/sandbox"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/syncclient"
)

// User preferences live in the user's synced settings document (scope USER, kind SETTINGS, key "settings"):
// sessions read them through the settings hierarchy like every other settings key, on every machine of the
// user. Get and Update read and write that document on the server; Update merges its keys into the document
// with a version check, keeping every other key as it is.

// PreferenceDocs is the part of harness.sync.v1.SyncService that reads and writes the user's settings document
// (implemented by *syncclient.Client).
type PreferenceDocs interface {
	GetDocument(ctx context.Context, req *syncv1.GetDocumentRequest) (*syncv1.GetDocumentResponse, error)
	PutDocument(ctx context.Context, req *syncv1.PutDocumentRequest) (*syncv1.PutDocumentResponse, error)
}

// preferenceAttempts bounds the read-merge-write rounds of UpdateUserPreferences on version conflicts.
const preferenceAttempts = 3

var userSettingsRef = &syncv1.DocumentRef{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, Key: settings.UserSettingsKey}

func (s *Server) GetUserPreferences(ctx context.Context, _ *connect.Request[agentv1.GetUserPreferencesRequest]) (*connect.Response[agentv1.GetUserPreferencesResponse], error) {
	if s.Preferences == nil {
		return connect.NewResponse(&agentv1.GetUserPreferencesResponse{Preferences: s.withManagedPolicy(preferencesOf(nil))}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.userSettings(ctx)
	if err != nil {
		return nil, syncError(err)
	}
	return connect.NewResponse(&agentv1.GetUserPreferencesResponse{Preferences: s.withManagedPolicy(preferencesOf(doc))}), nil
}

// withManagedPolicy adds what the organization imposes, which no preference can change.
func (s *Server) withManagedPolicy(p *agentv1.UserPreferences) *agentv1.UserPreferences {
	m := s.Base.ModePolicy.Managed()
	p.ManagedPolicy = &agentv1.ManagedPolicyView{
		Active: m.Active(), AllowRules: m.AllowRules, AskRules: m.AskRules, DenyRules: m.DenyRules, ManagedRulesOnly: m.ManagedRulesOnly,
		SandboxRequired: m.SandboxRequired, SandboxOutsideRefused: m.SandboxOutsideRefused, DisabledModes: m.DisabledModes,
		RestrictProviders: m.RestrictProviders, AllowedProviders: m.AllowedProviders, PersonalProvidersDisabled: m.PersonalProvidersDisabled,
		RestrictMcpServers: m.RestrictMcpServers, AllowedMcpServers: m.AllowedMcpServers, LocalModelsDisabled: m.LocalModelsDisabled,
	}
	return p
}

func (s *Server) UpdateUserPreferences(ctx context.Context, req *connect.Request[agentv1.UpdateUserPreferencesRequest]) (*connect.Response[agentv1.UpdateUserPreferencesResponse], error) {
	if s.Preferences == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("preferences are stored on the harness server; this client is not connected to one"))
	}
	set := map[string]any{}
	if req.Msg.CompactIdleSessions != nil {
		set[settings.KeyCompactIdleSessions] = req.Msg.GetCompactIdleSessions()
	}
	if req.Msg.AutoMemoryEnabled != nil {
		set[settings.KeyAutoMemoryEnabled] = req.Msg.GetAutoMemoryEnabled()
	}
	if req.Msg.LspEnabled != nil {
		set[settings.KeyLSPEnabled] = req.Msg.GetLspEnabled()
	}
	if req.Msg.LspDiagnostics != nil {
		set[settings.KeyLSPDiagnostics] = req.Msg.GetLspDiagnostics()
	}
	if req.Msg.LspDiagnosticsStartServer != nil {
		set[settings.KeyLSPDiagnosticsStartServer] = req.Msg.GetLspDiagnosticsStartServer()
	}
	if req.Msg.SchedulesEnabled != nil {
		set[settings.KeySchedulesEnabled] = req.Msg.GetSchedulesEnabled()
		defer s.scheduler().forgetPreference()
	}
	if req.Msg.GoalMaxTurns != nil {
		n := int(req.Msg.GetGoalMaxTurns())
		if !settings.ValidGoalMaxTurns(n) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the turns of a goal must be between 1 and %d", settings.MaxGoalMaxTurns))
		}
		set[settings.KeyGoalMaxTurns] = n
	}
	if req.Msg.PrMonitorWakesPerHour != nil {
		n := int(req.Msg.GetPrMonitorWakesPerHour())
		if !settings.ValidPRMonitorWakesPerHour(n) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("wakes per hour must be between 1 and %d", settings.MaxPRMonitorWakesPerHour))
		}
		set[settings.KeyPRMonitorWakesPerHour] = n
	}
	if req.Msg.SubagentMaxDepth != nil {
		n := int(req.Msg.GetSubagentMaxDepth())
		if !settings.ValidSubagentMaxDepth(n) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agent nesting depth must be between 1 and %d", settings.MaxSubagentMaxDepth))
		}
		set[settings.KeySubagentMaxDepth] = n
	}
	if req.Msg.SubagentMaxRunning != nil {
		n := int(req.Msg.GetSubagentMaxRunning())
		if !settings.ValidSubagentMaxRunning(n) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agents running at once must be between 1 and %d", settings.MaxSubagentMaxRunning))
		}
		set[settings.KeySubagentMaxRunning] = n
	}
	if req.Msg.WorkflowMaxAgents != nil {
		n := int(req.Msg.GetWorkflowMaxAgents())
		if !settings.ValidWorkflowMaxAgents(n) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("agents per workflow must be between 1 and %d", settings.MaxWorkflowMaxAgents))
		}
		set[settings.KeyWorkflowMaxAgents] = n
	}
	if req.Msg.HooksEnabled != nil {
		set[settings.KeyDisableAllHooks] = !req.Msg.GetHooksEnabled()
	}
	if b := req.Msg.Sandbox; b != nil {
		for _, p := range b.GetAllowedWritePaths() {
			if !settings.ValidSandboxPath(p) {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("write path %q must be absolute or start with ~/", p))
			}
		}
		for _, h := range b.GetAllowedHosts() {
			if !sandbox.ValidHost(h) {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a host name, *.domain or either with a :port", h))
			}
		}
		set[settings.KeySandbox] = map[string]any{"enabled": b.GetEnabled(), "allowedWritePaths": nonNil(b.GetAllowedWritePaths()), "allowedHosts": nonNil(b.GetAllowedHosts()),
			"allowLocalhost": b.GetAllowLocalhost(), "autoAllowBash": b.GetAutoAllowBash(), "outsideWithoutPrompt": b.GetOutsideWithoutPrompt()}
	}
	if t := req.Msg.ModelTuning; t != nil {
		if !settings.ValidSpeedTier(t.GetSpeedTier()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown speed tier %q", t.GetSpeedTier()))
		}
		if !settings.ValidCacheTTL(t.GetCacheTtl()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown cache lifetime %q", t.GetCacheTtl()))
		}
		set[settings.KeySpeedTier], set[settings.KeyCacheTTL], set[settings.KeyAdvisorModel] = t.GetSpeedTier(), t.GetCacheTtl(), t.GetAdvisorModel()
	}
	if sw := req.Msg.SkillSwitch; sw != nil {
		if strings.TrimSpace(sw.GetName()) == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the skill's name is empty"))
		}
		set[settings.KeyDisabledSkills] = flipDisabled(sw.GetName(), sw.GetEnabled())
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.mergeUserSettings(ctx, set)
	if err != nil {
		return nil, err
	}
	if sw := req.Msg.SkillSwitch; sw != nil {
		s.applySkillSwitch(sw.GetName(), sw.GetEnabled())
	}
	return connect.NewResponse(&agentv1.UpdateUserPreferencesResponse{Preferences: s.withManagedPolicy(preferencesOf(doc))}), nil
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// GetSandboxStatus says what this machine can do; the user's choice is in the preferences.
func (s *Server) GetSandboxStatus(context.Context, *connect.Request[agentv1.GetSandboxStatusRequest]) (*connect.Response[agentv1.GetSandboxStatusResponse], error) {
	info := sandbox.NewDriver().Info()
	required, refused := s.Base.ModePolicy.Sandbox()
	return connect.NewResponse(&agentv1.GetSandboxStatusResponse{Platform: info.Platform, Supported: info.Supported, Reason: info.Reason,
		RequiredByPolicy: required, OutsideRefusedByPolicy: refused, BuiltinHosts: sandbox.DefaultHosts}), nil
}

// mergeUserSettings writes keys into the user's settings document, keeping the others, and returns the document.
func (s *Server) mergeUserSettings(ctx context.Context, set map[string]any) (*syncv1.Document, error) {
	doc, err := s.userSettings(ctx)
	if err != nil {
		return nil, syncError(err)
	}
	for i := 0; len(set) > 0 && i < preferenceAttempts; i++ {
		content, err := mergeSettingsJSON(doc.GetContent(), set)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the user settings document is not a JSON object: %w", err))
		}
		res, err := s.Preferences.PutDocument(ctx, &syncv1.PutDocumentRequest{Ref: userSettingsRef, Content: content, BaseVersion: proto.Int64(doc.GetVersion()), Source: "client:preferences"})
		if err != nil {
			return nil, syncError(err)
		}
		if d := res.GetDocument(); d != nil {
			doc, set = d, nil
			break
		}
		// A conflict: merge again over the current document (a tombstone starts over from nothing).
		doc = res.GetConflict().GetDocument()
	}
	if len(set) > 0 {
		return nil, connect.NewError(connect.CodeAborted, errors.New("the user settings document kept changing; try again"))
	}
	return doc, nil
}

// userSettings returns the user's settings document, nil when there is none.
func (s *Server) userSettings(ctx context.Context) (*syncv1.Document, error) {
	res, err := s.Preferences.GetDocument(ctx, &syncv1.GetDocumentRequest{Selector: &syncv1.GetDocumentRequest_Ref{Ref: userSettingsRef}})
	if err != nil {
		if syncclient.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return res.GetDocument(), nil
}

// preferencesOf reads the preferences from a settings document (nil or invalid content: the defaults).
func preferencesOf(doc *syncv1.Document) *agentv1.UserPreferences {
	p := &agentv1.UserPreferences{CompactIdleSessions: true, LspEnabled: true, LspDiagnostics: true, PrMonitorWakesPerHour: settings.DefaultPRMonitorWakesPerHour,
		SubagentMaxDepth: settings.DefaultSubagentMaxDepth, SubagentMaxRunning: settings.DefaultSubagentMaxRunning, WorkflowMaxAgents: settings.DefaultWorkflowMaxAgents,
		Sandbox: &agentv1.SandboxPreferences{Enabled: true}, HooksEnabled: true, AutoMemoryEnabled: true, SchedulesEnabled: true, GoalMaxTurns: settings.DefaultGoalMaxTurns}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(doc.GetContent()), &raw) != nil {
		return p
	}
	var b bool
	if v, ok := raw[settings.KeySchedulesEnabled]; ok && json.Unmarshal(v, &b) == nil {
		p.SchedulesEnabled = b
	}
	var turns int
	if v, ok := raw[settings.KeyGoalMaxTurns]; ok && json.Unmarshal(v, &turns) == nil && settings.ValidGoalMaxTurns(turns) {
		p.GoalMaxTurns = int32(turns)
	}
	if v, ok := raw[settings.KeyCompactIdleSessions]; ok && json.Unmarshal(v, &b) == nil {
		p.CompactIdleSessions = b
	}
	for key, dst := range map[string]*bool{settings.KeyAutoMemoryEnabled: &p.AutoMemoryEnabled, settings.KeyLSPEnabled: &p.LspEnabled, settings.KeyLSPDiagnostics: &p.LspDiagnostics, settings.KeyLSPDiagnosticsStartServer: &p.LspDiagnosticsStartServer} {
		if v, ok := raw[key]; ok && json.Unmarshal(v, &b) == nil {
			*dst = b
		}
	}
	if v, ok := raw[settings.KeyDisabledSkills]; ok {
		_ = json.Unmarshal(v, &p.DisabledSkills)
	}
	if v, ok := raw[settings.KeyDisableAllHooks]; ok && json.Unmarshal(v, &b) == nil {
		p.HooksEnabled = !b
	}
	p.ModelTuning = &agentv1.ModelTuning{}
	for key, dst := range map[string]*string{settings.KeySpeedTier: &p.ModelTuning.SpeedTier, settings.KeyCacheTTL: &p.ModelTuning.CacheTtl, settings.KeyAdvisorModel: &p.ModelTuning.AdvisorModel} {
		if v, ok := raw[key]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	var n int32
	if v, ok := raw[settings.KeyPRMonitorWakesPerHour]; ok && json.Unmarshal(v, &n) == nil && settings.ValidPRMonitorWakesPerHour(int(n)) {
		p.PrMonitorWakesPerHour = n
	}
	if v, ok := raw[settings.KeySubagentMaxDepth]; ok && json.Unmarshal(v, &n) == nil && settings.ValidSubagentMaxDepth(int(n)) {
		p.SubagentMaxDepth = n
	}
	if v, ok := raw[settings.KeySubagentMaxRunning]; ok && json.Unmarshal(v, &n) == nil && settings.ValidSubagentMaxRunning(int(n)) {
		p.SubagentMaxRunning = n
	}
	if v, ok := raw[settings.KeyWorkflowMaxAgents]; ok && json.Unmarshal(v, &n) == nil && settings.ValidWorkflowMaxAgents(int(n)) {
		p.WorkflowMaxAgents = n
	}
	if v, ok := raw[settings.KeySandbox]; ok {
		var sb struct {
			Enabled              *bool    `json:"enabled"`
			AllowedWritePaths    []string `json:"allowedWritePaths"`
			AllowedHosts         []string `json:"allowedHosts"`
			AllowLocalhost       bool     `json:"allowLocalhost"`
			AutoAllowBash        bool     `json:"autoAllowBash"`
			OutsideWithoutPrompt bool     `json:"outsideWithoutPrompt"`
		}
		if json.Unmarshal(v, &sb) == nil {
			p.Sandbox = &agentv1.SandboxPreferences{Enabled: sb.Enabled == nil || *sb.Enabled, AllowedWritePaths: sb.AllowedWritePaths, AllowedHosts: sb.AllowedHosts,
				AllowLocalhost: sb.AllowLocalhost, AutoAllowBash: sb.AutoAllowBash, OutsideWithoutPrompt: sb.OutsideWithoutPrompt}
		}
	}
	return p
}

// mergeSettingsJSON sets keys in a settings JSON object, keeping the values of the other keys (the document is
// re-indented, keys sorted). Empty content starts a new object.
func mergeSettingsJSON(content string, set map[string]any) (string, error) {
	raw := map[string]json.RawMessage{}
	if strings.TrimSpace(content) != "" {
		if err := json.Unmarshal([]byte(content), &raw); err != nil {
			return "", err
		}
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
	}
	for k, v := range set {
		if f, ok := v.(func(json.RawMessage) any); ok {
			v = f(raw[k])
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		raw[k] = b
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	return string(out), err
}
