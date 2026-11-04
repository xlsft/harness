package server

import (
	"context"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/usagelimit"
)

func TestSwitchModelRespectsThePolicyList(t *testing.T) {
	ls := &liveSession{srv: &Server{}, sess: &bootstrap.Session{Settings: &settings.Settings{AvailableModels: []string{"allowed"}}}}
	if _, err := ls.SwitchModel(context.Background(), "other"); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("%v", err)
	}
}

func TestSwitchModelRefusesAProviderOutOfUsage(t *testing.T) {
	left := 0.0
	ls := &liveSession{srv: &Server{}, sess: &bootstrap.Session{}}
	ls.usage.monitor = &usagelimit.Monitor{Fetch: func(context.Context, string) (*usagelimit.Snapshot, error) {
		return &usagelimit.Snapshot{ProviderName: "Acme", Balance: &usagelimit.Balance{Left: &left}}, nil
	}}
	if _, err := ls.SwitchModel(context.Background(), "other"); err == nil || !strings.Contains(err.Error(), "Acme is out of usage") {
		t.Fatalf("%v", err)
	}
}

func TestElsewhereReasonFollowsTheLastMessage(t *testing.T) {
	ls := &liveSession{}
	if ls.ElsewhereReason() != "" {
		t.Fatal("a local session has its user here")
	}
	if !isRelayOrigin(WithRemoteOrigin(context.Background())) || isRelayOrigin(context.Background()) {
		t.Fatal("the mark")
	}
	ls.drivenByRelay.Store(true)
	if !strings.Contains(ls.ElsewhereReason(), "another machine") {
		t.Fatal(ls.ElsewhereReason())
	}
	ls.drivenByRelay.Store(false)
	if ls.ElsewhereReason() != "" {
		t.Fatal("the user came back to this machine")
	}
}
