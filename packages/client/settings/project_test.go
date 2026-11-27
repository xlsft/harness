package settings

import (
	"context"
	"path/filepath"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

func TestProjectLimits(t *testing.T) {
	root := t.TempDir()
	cwd, home := filepath.Join(root, "p"), filepath.Join(root, "h")
	load := func(docs ...*syncv1.Document) *Settings {
		t.Helper()
		s, err := Load(context.Background(), Options{Cwd: cwd, Home: home, Docs: &fakeDocs{synced: true, docs: docs}, Trusted: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	user := func(c string) *syncv1.Document {
		return doc("u", commonv1.Scope_SCOPE_USER, syncv1.DocumentKind_DOCUMENT_KIND_SETTINGS, UserSettingsKey, c)
	}
	policy := func(c string) *syncv1.Document {
		return doc("p", commonv1.Scope_SCOPE_ORG, syncv1.DocumentKind_DOCUMENT_KIND_POLICY, "default", c)
	}
	s := load()
	if w, b := s.ProjectLimits(0, 0); w != DefaultProjectMaxWorkers || b != 0 || s.ProjectRunsDisabled {
		t.Fatalf("defaults %d %v", w, b)
	}
	s = load(user(`{"projectMaxWorkers": 10, "projectBudgetUsd": 5}`))
	if w, b := s.ProjectLimits(0, 0); w != 10 || b != 5 {
		t.Fatalf("user %d %v", w, b)
	}
	if w, b := s.ProjectLimits(2, 1); w != 2 || b != 1 {
		t.Fatalf("asked %d %v", w, b)
	}
	s = load(user(`{"projectMaxWorkers": 10, "projectBudgetUsd": 5}`), policy(`{"projectMaxWorkers": 3, "projectBudgetUsd": 2, "disableProjectRuns": false}`))
	if w, b := s.ProjectLimits(20, 50); w != 3 || b != 2 {
		t.Fatalf("policy ceiling %d %v", w, b)
	}
	if w, b := s.ProjectLimits(1, 0); w != 1 || b != 2 {
		t.Fatalf("below the ceiling %d %v", w, b)
	}
	if s = load(policy(`{"disableProjectRuns": true}`)); !s.ProjectRunsDisabled {
		t.Fatal("policy off")
	}
	if s = load(user(`{"disableProjectRuns": true}`)); s.ProjectRunsDisabled {
		t.Fatal("only a policy turns runs off")
	}
	if s = load(user(`{"projectMaxWorkers": 99}`)); s.ProjectMaxWorkers != DefaultProjectMaxWorkers {
		t.Fatalf("invalid value taken: %d", s.ProjectMaxWorkers)
	}
}
