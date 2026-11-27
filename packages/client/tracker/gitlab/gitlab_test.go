package gitlab

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

// fake is GitLab's REST API under /api/v4, enough for issues.
type fake struct {
	*httptest.Server
	mu      sync.Mutex
	puts    []map[string]any
	notes   []string
	queries []string
	paths   []string
}

func issue(iid int) map[string]any {
	return map[string]any{"iid": iid, "title": "Issue", "description": "Body", "web_url": "https://gl.test/grp/sub/app/-/issues/1", "state": "opened",
		"updated_at": "2026-01-02T03:04:05Z", "labels": []any{"bug"}, "assignees": []any{map[string]any{"id": 1, "username": "alice"}},
		"references": map[string]any{"full": "grp/sub/app#" + string(rune('0'+iid))}}
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gl-token" {
			w.WriteHeader(http.StatusUnauthorized)
			write(w, map[string]any{"message": "401 Unauthorized"})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		p := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/")
		const proj = "projects/grp%2Fsub%2Fapp"
		switch {
		case p == "user":
			write(w, map[string]any{"id": 1, "username": "alice", "name": "Alice"})
		case p == "users":
			write(w, []any{map[string]any{"id": 2, "username": r.URL.Query().Get("username")}})
		case p == "issues" || p == proj+"/issues" && r.Method == http.MethodGet:
			f.paths = append(f.paths, p)
			f.queries = append(f.queries, r.URL.RawQuery)
			write(w, []any{issue(1), issue(3)})
		case p == proj+"/issues/1" && r.Method == http.MethodGet:
			write(w, issue(1))
		case p == proj+"/issues/1" && r.Method == http.MethodPut:
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.puts = append(f.puts, b)
			write(w, issue(1))
		case p == proj+"/issues/1/notes" && r.Method == http.MethodGet:
			write(w, []any{
				map[string]any{"body": "newest", "created_at": "2026-01-03T00:00:00Z", "author": map[string]any{"username": "bob"}},
				map[string]any{"body": "changed the label", "system": true, "created_at": "2026-01-02T12:00:00Z", "author": map[string]any{"username": "bob"}},
				map[string]any{"body": "first", "created_at": "2026-01-02T00:00:00Z", "author": map[string]any{"username": "carol"}},
			})
		case p == proj+"/issues/1/notes":
			var b struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.notes = append(f.notes, b.Body)
			write(w, map[string]any{"id": 1})
		case p == proj+"/issues" && r.Method == http.MethodPost:
			write(w, issue(7))
		case p == proj+"/milestones":
			write(w, []any{map[string]any{"id": 40, "title": "v1"}})
		case p == proj+"/labels":
			write(w, []any{map[string]any{"name": "bug"}, map[string]any{"name": "review"}})
		case p == proj+"/members/all":
			write(w, []any{map[string]any{"id": 1, "username": "alice"}})
		case p == "projects":
			write(w, []any{map[string]any{"path_with_namespace": "grp/sub/app", "name": "app"}})
		default:
			w.WriteHeader(http.StatusNotFound)
			write(w, map[string]any{"message": "404 Not Found"})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestGitLabIssues(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	var asked []string
	token := func(_ context.Context, host string) (string, error) {
		asked = append(asked, host)
		return "gl-token", nil
	}
	p, err := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthVCS, VCSToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if me, err := p.Test(ctx); err != nil || me.Login != "alice" || !strings.HasPrefix(asked[0], "127.0.0.1:") {
		t.Fatalf("test: %+v %v %v", me, err, asked)
	}
	issues, more, err := p.Search(ctx, `project:grp/sub/app state:open label:bug assignee:me "slow login"`, tracker.Page{Limit: 1})
	if err != nil || !more || len(issues) != 1 || issues[0].ID != "grp/sub/app#1" {
		t.Fatalf("search: %+v %v %v", issues, more, err)
	}
	if f.paths[0] != "projects/grp%2Fsub%2Fapp/issues" || !strings.Contains(f.queries[0], "assignee_username=alice") || !strings.Contains(f.queries[0], "search=slow+login") || !strings.Contains(f.queries[0], "state=opened") {
		t.Errorf("query: %s %s", f.paths[0], f.queries[0])
	}
	if _, _, err := p.Search(ctx, "colour:red", tracker.Page{Limit: 5}); err == nil {
		t.Error("an unknown filter was taken")
	}
	got, err := p.Get(ctx, "grp/sub/app#1")
	if err != nil || got.Project != "grp/sub/app" || len(got.Comments) != 2 || got.Comments[0].Text != "first" || got.Comments[1].Author != "bob" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := p.Get(ctx, "grp/sub/app#404"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := p.Get(ctx, "app#1"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	err = p.Update(ctx, "grp/sub/app#1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "State", Value: "closed"}, {Name: "Add labels", Value: "review"}, {Name: "Remove labels", Value: "bug"}, {Name: "Assignees", Value: "me, bob"}, {Name: "Milestone", Value: "v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	pt := f.puts[0]
	if pt["state_event"] != "close" || pt["add_labels"] != "review" || pt["remove_labels"] != "bug" || pt["milestone_id"] != 40.0 || len(pt["assignee_ids"].([]any)) != 2 {
		t.Fatalf("put: %v", pt)
	}
	if err := p.Update(ctx, "grp/sub/app#1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "State", Value: "limbo"}}}); err == nil {
		t.Fatal("bad state accepted")
	}
	if err := p.Comment(ctx, "grp/sub/app#1", "shipped"); err != nil || f.notes[0] != "shipped" {
		t.Fatalf("comment: %v", err)
	}
	if c, err := p.Create(ctx, tracker.IssueDraft{Project: "grp/sub/app", Title: "New"}); err != nil || c.ID != "grp/sub/app#7" {
		t.Fatalf("create: %+v %v", c, err)
	}
	fields, err := p.Fields(ctx, "grp/sub/app")
	if err != nil || len(fields) != 6 || strings.Join(fields[1].Values, ",") != "bug,review" || fields[5].Values[0] != "v1" {
		t.Fatalf("fields: %+v %v", fields, err)
	}
	if ps, err := p.Projects(ctx); err != nil || len(ps) != 1 || ps[0].Key != "grp/sub/app" {
		t.Fatalf("projects: %+v %v", ps, err)
	}
	noLogin := func(context.Context, string) (string, error) { return "", tracker.ErrAuth }
	q, _ := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthVCS, VCSToken: noLogin})
	if _, err := q.Test(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Fatalf("no glab login: %v", err)
	}
}

func TestParseQuery(t *testing.T) {
	f, err := ParseQuery("group:grp author:bob")
	if err != nil || f.Path != "groups/grp/issues" || f.Params.Get("author_username") != "bob" || f.Params.Get("scope") != "" {
		t.Fatalf("%+v %v", f, err)
	}
	if f, _ := ParseQuery("crash"); f.Path != "issues" || f.Params.Get("scope") != "all" || f.Params.Get("search") != "crash" {
		t.Fatalf("%+v", f)
	}
}
