package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/tracker"
)

// fake is GitHub Enterprise Server's REST API under /api/v3, enough for issues.
type fake struct {
	*httptest.Server
	mu      sync.Mutex
	patches []map[string]any
	added   []any
	removed []string
	posted  []string
	queries []string
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func item(n int, pr bool) map[string]any {
	m := map[string]any{"number": n, "title": "Issue", "body": "Body", "html_url": "https://ghe.test/octo/app/issues/1", "state": "open", "updated_at": "2026-01-02T03:04:05Z",
		"repository_url": "https://ghe.test/api/v3/repos/octo/app", "labels": []any{map[string]any{"name": "bug"}}, "assignees": []any{map[string]any{"login": "alice"}}}
	if pr {
		m["pull_request"] = map[string]any{"url": "x"}
	}
	return m
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer gh-token" {
		w.WriteHeader(http.StatusUnauthorized)
		write(w, map[string]any{"message": "Bad credentials"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	switch {
	case p == "user":
		write(w, map[string]any{"login": "alice", "name": "Alice"})
	case p == "search/issues":
		f.queries = append(f.queries, r.URL.Query().Get("q"))
		write(w, map[string]any{"total_count": 3, "items": []any{item(1, false), item(2, true), item(3, false)}})
	case p == "repos/octo/app/issues/1" && r.Method == http.MethodGet:
		write(w, item(1, false))
	case p == "repos/octo/app/issues/1" && r.Method == http.MethodPatch:
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.patches = append(f.patches, b)
		write(w, item(1, false))
	case p == "repos/octo/app/issues/1/comments" && r.Method == http.MethodGet:
		write(w, []any{map[string]any{"body": "hi", "created_at": "2026-01-02T03:04:05Z", "user": map[string]any{"login": "bob"}}})
	case p == "repos/octo/app/issues/1/comments":
		var b struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.posted = append(f.posted, b.Body)
		write(w, map[string]any{"id": 1})
	case p == "repos/octo/app/issues/1/labels" && r.Method == http.MethodPost:
		var b struct{ Labels []any }
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.added = append(f.added, b.Labels...)
		write(w, []any{})
	case strings.HasPrefix(p, "repos/octo/app/issues/1/labels/") && r.Method == http.MethodDelete:
		f.removed = append(f.removed, strings.TrimPrefix(p, "repos/octo/app/issues/1/labels/"))
		if strings.HasSuffix(p, "gone") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		write(w, []any{})
	case p == "repos/octo/app/milestones":
		write(w, []any{map[string]any{"number": 4, "title": "v1"}})
	case p == "repos/octo/app/labels":
		write(w, []any{map[string]any{"name": "bug"}, map[string]any{"name": "review"}})
	case p == "repos/octo/app/assignees":
		write(w, []any{map[string]any{"login": "alice"}})
	case p == "repos/octo/app/issues" && r.Method == http.MethodPost:
		write(w, item(7, false))
	case p == "user/repos":
		write(w, []any{map[string]any{"full_name": "octo/app", "name": "app", "has_issues": true}, map[string]any{"full_name": "octo/old", "archived": true, "has_issues": true}})
	case p == "repos/octo/app/issues/404":
		w.WriteHeader(http.StatusNotFound)
		write(w, map[string]any{"message": "Not Found"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestGitHubIssues(t *testing.T) {
	f := newFake(t)
	var asked []string
	token := func(_ context.Context, host string) (string, error) {
		asked = append(asked, host)
		return "gh-token", nil
	}
	ctx := context.Background()
	p, err := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthVCS, VCSToken: token})
	if err != nil {
		t.Fatal(err)
	}
	me, err := p.Test(ctx)
	if err != nil || me.Login != "alice" || len(asked) != 1 || !strings.HasPrefix(asked[0], "127.0.0.1:") {
		t.Fatalf("test: %+v %v %v", me, err, asked)
	}
	issues, more, err := p.Search(ctx, "repo:octo/app is:open", tracker.Page{Limit: 10})
	if err != nil || more || len(issues) != 2 || issues[0].ID != "octo/app#1" || issues[1].ID != "octo/app#3" || f.queries[0] != "repo:octo/app is:open is:issue" {
		t.Fatalf("search: %+v %v %v %v", issues, more, err, f.queries)
	}
	got, err := p.Get(ctx, "octo/app#1")
	if err != nil || got.Project != "octo/app" || len(got.Comments) != 1 || got.Comments[0].Author != "bob" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := p.Get(ctx, "octo/app#404"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := p.Get(ctx, "nonsense"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	err = p.Update(ctx, "octo/app#1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "State", Value: "closed as not planned"}, {Name: "Add labels", Value: "review"}, {Name: "Remove labels", Value: "bug, gone"}, {Name: "Assignees", Value: "me, bob"}, {Name: "Milestone", Value: "v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	pt := f.patches[0]
	if pt["state"] != "closed" || pt["state_reason"] != "not_planned" || pt["milestone"] != 4.0 || len(pt["assignees"].([]any)) != 2 || pt["assignees"].([]any)[0] != "alice" {
		t.Fatalf("patch: %v", pt)
	}
	if len(f.added) != 1 || f.added[0] != "review" || strings.Join(f.removed, ",") != "bug,gone" {
		t.Fatalf("labels: %v %v", f.added, f.removed)
	}
	if err := p.Update(ctx, "octo/app#1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "State", Value: "limbo"}}}); err == nil {
		t.Fatal("bad state accepted")
	}
	if err := p.Comment(ctx, "octo/app#1", "shipped"); err != nil || f.posted[0] != "shipped" {
		t.Fatalf("comment: %v", err)
	}
	created, err := p.Create(ctx, tracker.IssueDraft{Project: "octo/app", Title: "New"})
	if err != nil || created.ID != "octo/app#7" {
		t.Fatalf("create: %+v %v", created, err)
	}
	fields, err := p.Fields(ctx, "octo/app")
	if err != nil || len(fields) != 6 || strings.Join(fields[1].Values, ",") != "bug,review" || fields[5].Values[0] != "v1" {
		t.Fatalf("fields: %+v %v", fields, err)
	}
	projects, err := p.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Key != "octo/app" {
		t.Fatalf("projects: %+v %v", projects, err)
	}

	noLogin := func(context.Context, string) (string, error) { return "", tracker.ErrAuth }
	q, _ := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthVCS, VCSToken: noLogin})
	if _, err := q.Test(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Fatalf("no gh login: %v", err)
	}
}

func TestAPIBase(t *testing.T) {
	if APIBase("https://github.com") != "https://api.github.com" || APIBase("https://ghe.example.com") != "https://ghe.example.com/api/v3" {
		t.Fatal(APIBase("https://github.com"))
	}
	if SearchQuery("is:pr author:me") != "is:pr author:me" {
		t.Fatal("is:pr was changed")
	}
}
