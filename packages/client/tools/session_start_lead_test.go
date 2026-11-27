package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionStartLead(t *testing.T) {
	env := &Env{Cwd: t.TempDir()}
	in := func(extra string) json.RawMessage {
		return json.RawMessage(`{"title":"API","prompt":"brief","initiation":"user_asked","context":"fresh","worktree":false` + extra + `}`)
	}
	_, r, err := parseSessionStart(env, in(`,"lead":true,"max_helpers":3,"budget_usd":7.5`))
	if err != nil || !r.Lead || r.MaxHelpers != 3 || r.BudgetUSD != 7.5 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, _, err := parseSessionStart(env, in(`,"max_helpers":3`)); err == nil || !strings.Contains(err.Error(), "go with lead") {
		t.Fatalf("limits without lead: %v", err)
	}
	if _, _, err := parseSessionStart(env, in(`,"lead":true,"budget_usd":-1`)); err == nil {
		t.Fatal("a negative budget was taken")
	}
}
