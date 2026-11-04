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
		return connect.NewResponse(&agentv1.GetUserPreferencesResponse{Preferences: preferencesOf(nil)}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.userSettings(ctx)
	if err != nil {
		return nil, syncError(err)
	}
	return connect.NewResponse(&agentv1.GetUserPreferencesResponse{Preferences: preferencesOf(doc)}), nil
}

func (s *Server) UpdateUserPreferences(ctx context.Context, req *connect.Request[agentv1.UpdateUserPreferencesRequest]) (*connect.Response[agentv1.UpdateUserPreferencesResponse], error) {
	if s.Preferences == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("preferences are stored on the harness server; this client is not connected to one"))
	}
	set := map[string]any{}
	if req.Msg.CompactIdleSessions != nil {
		set[settings.KeyCompactIdleSessions] = req.Msg.GetCompactIdleSessions()
	}
	if req.Msg.PrMonitorWakesPerHour != nil {
		n := int(req.Msg.GetPrMonitorWakesPerHour())
		if !settings.ValidPRMonitorWakesPerHour(n) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("wakes per hour must be between 1 and %d", settings.MaxPRMonitorWakesPerHour))
		}
		set[settings.KeyPRMonitorWakesPerHour] = n
	}
	ctx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	defer cancel()
	doc, err := s.mergeUserSettings(ctx, set)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.UpdateUserPreferencesResponse{Preferences: preferencesOf(doc)}), nil
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
	p := &agentv1.UserPreferences{CompactIdleSessions: true, PrMonitorWakesPerHour: settings.DefaultPRMonitorWakesPerHour}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(doc.GetContent()), &raw) != nil {
		return p
	}
	var b bool
	if v, ok := raw[settings.KeyCompactIdleSessions]; ok && json.Unmarshal(v, &b) == nil {
		p.CompactIdleSessions = b
	}
	var n int32
	if v, ok := raw[settings.KeyPRMonitorWakesPerHour]; ok && json.Unmarshal(v, &n) == nil && settings.ValidPRMonitorWakesPerHour(int(n)) {
		p.PrMonitorWakesPerHour = n
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
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		raw[k] = b
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	return string(out), err
}
