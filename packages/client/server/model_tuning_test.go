package server

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

func TestModelTuningIsAPreference(t *testing.T) {
	ctx := context.Background()
	docs := &fakeDocs{}
	s := &Server{Preferences: docs}
	docs.write(`{"model":"big"}`)

	got, err := s.GetUserPreferences(ctx, connect.NewRequest(&agentv1.GetUserPreferencesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Msg.Preferences.GetModelTuning(); m.GetSpeedTier() != "" || m.GetCacheTtl() != "" || m.GetAdvisorModel() != "" {
		t.Errorf("defaults: %+v", m)
	}

	up, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{ModelTuning: &agentv1.ModelTuning{SpeedTier: "fast", CacheTtl: "1h", AdvisorModel: "wise"}}))
	if err != nil {
		t.Fatal(err)
	}
	if m := up.Msg.Preferences.GetModelTuning(); m.GetSpeedTier() != "fast" || m.GetCacheTtl() != "1h" || m.GetAdvisorModel() != "wise" {
		t.Errorf("after update: %+v", m)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(docs.doc.GetContent()), &stored); err != nil {
		t.Fatal(err)
	}
	if stored["speedTier"] != "fast" || stored["cacheTtl"] != "1h" || stored["advisorModel"] != "wise" || stored["model"] != "big" {
		t.Errorf("stored: %v", stored)
	}

	for name, m := range map[string]*agentv1.ModelTuning{"tier": {SpeedTier: "warp"}, "lifetime": {CacheTtl: "1y"}} {
		_, err := s.UpdateUserPreferences(ctx, connect.NewRequest(&agentv1.UpdateUserPreferencesRequest{ModelTuning: m}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}
