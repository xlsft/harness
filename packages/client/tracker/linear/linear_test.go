package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/tracker"
)

const apiKey = "lin_api_1"

// fake answers the GraphQL operations the provider sends, told apart by their text.
type fake struct {
	*httptest.Server
	mu       sync.Mutex
	filters  []any
	updates  []map[string]any
	comments []map[string]any
	state    map[string]string
}

func newFake(t *testing.T) *fake {
	f := &fake{state: map[string]string{"ENG-1": "Todo", "ENG-2": "Todo", "ENG-3": "In Progress"}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func node(id string, state string) map[string]any {
	return map[string]any{"id": "uuid-" + id, "identifier": id, "title": "Issue " + id, "url": "https://linear.app/acme/issue/" + id, "description": "Body", "updatedAt": "2026-01-02T03:04:05.000Z", "priority": 2,
		"team": map[string]any{"id": "team-1", "key": "ENG"}, "state": map[string]any{"name": state}, "assignee": map[string]any{"name": "Alice"},
		"labels":   map[string]any{"nodes": []any{map[string]any{"id": "lab-bug", "name": "Bug"}}},
		"comments": map[string]any{"nodes": []any{map[string]any{"body": "hi", "createdAt": "2026-01-02T03:04:05.000Z", "user": map[string]any{"name": "Bob"}}}}}
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/oauth/token" {
		_ = r.ParseForm()
		if r.PostForm.Get("code") == "code-1" || r.PostForm.Get("grant_type") == "refresh_token" {
			write(w, map[string]any{"access_token": "oauth-access", "refresh_token": "oauth-refresh", "expires_in": 86400})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		write(w, map[string]any{"error": "invalid_grant"})
		return
	}
	if a := r.Header.Get("Authorization"); a != apiKey && a != "Bearer oauth-access" {
		w.WriteHeader(http.StatusBadRequest)
		write(w, map[string]any{"errors": []any{map[string]any{"message": "Authentication required, not authenticated", "extensions": map[string]any{"code": "AUTHENTICATION_ERROR"}}}})
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	q := req.Query
	switch {
	case strings.Contains(q, "issueUpdate"):
		in := req.Variables["in"].(map[string]any)
		f.updates = append(f.updates, in)
		if in["stateId"] == "st-progress" {
			f.state["ENG-1"] = "In Progress"
		}
		write(w, map[string]any{"data": map[string]any{"issueUpdate": map[string]any{"success": true}}})
	case strings.Contains(q, "commentCreate"):
		f.comments = append(f.comments, req.Variables["in"].(map[string]any))
		write(w, map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
	case strings.Contains(q, "issueCreate"):
		write(w, map[string]any{"data": map[string]any{"issueCreate": map[string]any{"success": true, "issue": node("ENG-9", "Todo")}}})
	case strings.Contains(q, "issues("):
		f.filters = append(f.filters, req.Variables["filter"])
		ids := []string{"ENG-1", "ENG-2", "ENG-3"}
		after, _ := req.Variables["after"].(string)
		start := 0
		for i, id := range ids {
			if id == after {
				start = i + 1
			}
		}
		// Two a page, to exercise the cursor.
		end := min(start+2, len(ids))
		var nodes []any
		for _, id := range ids[start:end] {
			nodes = append(nodes, node(id, f.state[id]))
		}
		write(w, map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": end < len(ids), "endCursor": ids[end-1]}}}})
	case strings.Contains(q, "issue(id"):
		id := req.Variables["id"].(string)
		if _, ok := f.state[id]; !ok {
			write(w, map[string]any{"errors": []any{map[string]any{"message": "Entity not found: Issue", "extensions": map[string]any{"type": "invalid input"}}}})
			return
		}
		write(w, map[string]any{"data": map[string]any{"issue": node(id, f.state[id])}})
	case strings.Contains(q, "teams("):
		write(w, map[string]any{"data": map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{
			"id": "team-1", "key": "ENG", "name": "Engineering", "description": "Builds",
			"states":  map[string]any{"nodes": []any{map[string]any{"id": "st-todo", "name": "Todo"}, map[string]any{"id": "st-progress", "name": "In Progress"}}},
			"labels":  map[string]any{"nodes": []any{map[string]any{"id": "lab-bug", "name": "Bug"}, map[string]any{"id": "lab-review", "name": "Review"}}},
			"members": map[string]any{"nodes": []any{map[string]any{"id": "u-bob", "name": "Bob", "displayName": "bob", "email": "bob@example.test"}}},
		}}}}})
	case strings.Contains(q, "viewer"):
		write(w, map[string]any{"data": map[string]any{"viewer": map[string]any{"id": "u-alice", "name": "Alice", "displayName": "alice", "email": "alice@example.test"}}})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestLinear(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	p, err := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthToken, Credentials: tracker.Credentials{Secret: apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	me, err := p.Test(ctx)
	if err != nil || me.Login != "alice@example.test" || me.DisplayName != "Alice" {
		t.Fatalf("test: %+v %v", me, err)
	}
	issues, more, err := p.Search(ctx, `team:ENG state:"Todo" assignee:me`, tracker.Page{Limit: 10})
	if err != nil || more || len(issues) != 3 || issues[0].ID != "ENG-1" || issues[0].Project != "ENG" {
		t.Fatalf("search: %+v %v %v", issues, more, err)
	}
	b, _ := json.Marshal(f.filters[0])
	if !strings.Contains(string(b), `"isMe":{"eq":true}`) || !strings.Contains(string(b), `"key":{"eqIgnoreCase":"ENG"}`) {
		t.Fatalf("filter: %s", b)
	}
	issues, more, err = p.Search(ctx, `{"team":{"key":{"eq":"ENG"}}}`, tracker.Page{Limit: 1, Offset: 1})
	if err != nil || !more || len(issues) != 1 || issues[0].ID != "ENG-2" {
		t.Fatalf("json filter with offset: %+v %v %v", issues, more, err)
	}
	got, err := p.Get(ctx, "ENG-1")
	if err != nil || got.Description != "Body" || len(got.Comments) != 1 || !hasField(got.Fields, "Priority", "High") || !hasField(got.Fields, "State", "Todo") {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := p.Get(ctx, "ENG-404"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := p.Update(ctx, "ENG-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "State", Value: "in progress"}, {Name: "Add labels", Value: "Review"}, {Name: "Assignee", Value: "bob@example.test"}, {Name: "Priority", Value: "urgent"}}}); err != nil {
		t.Fatal(err)
	}
	up := f.updates[0]
	if up["stateId"] != "st-progress" || up["assigneeId"] != "u-bob" || up["priority"] != 1.0 || len(up["labelIds"].([]any)) != 2 {
		t.Fatalf("update: %v", up)
	}
	if err := p.Update(ctx, "ENG-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "State", Value: "Nope"}}}); err == nil || !strings.Contains(err.Error(), "Todo, In Progress") {
		t.Fatalf("bad state: %v", err)
	}
	if err := p.Comment(ctx, "ENG-1", "done"); err != nil || f.comments[0]["issueId"] != "uuid-ENG-1" {
		t.Fatalf("comment: %v %v", err, f.comments)
	}
	created, err := p.Create(ctx, tracker.IssueDraft{Project: "ENG", Title: "New"})
	if err != nil || created.ID != "ENG-9" {
		t.Fatalf("create: %+v %v", created, err)
	}
	fields, err := p.Fields(ctx, "ENG")
	if err != nil || fields[0].Name != "State" || strings.Join(fields[0].Values, ",") != "Todo,In Progress" {
		t.Fatalf("fields: %+v %v", fields, err)
	}
	projects, err := p.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Key != "ENG" {
		t.Fatalf("projects: %+v %v", projects, err)
	}
	bad, _ := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthToken, Credentials: tracker.Credentials{Secret: "wrong"}})
	if _, err := bad.Test(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestOAuth(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	ex := oauthExchange{api: apiBase(f.URL)}
	u, err := ex.AuthorizeURL(ctx, "cli", "http://127.0.0.1:1/callback", "st", "ch")
	if err != nil || !strings.HasPrefix(u, f.URL+"/oauth/authorize?") {
		t.Fatalf("authorize: %s %v", u, err)
	}
	if real, _ := (oauthExchange{api: DefaultBaseURL}).AuthorizeURL(ctx, "c", "r", "s", "x"); !strings.HasPrefix(real, "https://linear.app/oauth/authorize?") {
		t.Fatal(real)
	}
	creds, err := ex.Exchange(ctx, "cli", "", "code-1", "http://127.0.0.1:1/callback", "ver")
	if err != nil || creds.AccessToken != "oauth-access" {
		t.Fatalf("exchange: %+v %v", creds, err)
	}
	creds.ExpiresAt = 1
	var stored tracker.Credentials
	p, _ := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthOAuth, OAuthClientID: "cli", Credentials: creds, OnTokens: func(c tracker.Credentials) { stored = c }})
	if _, err := p.Test(ctx); err != nil || stored.AccessToken != "oauth-access" {
		t.Fatalf("refresh: %v %+v", err, stored)
	}
}

func TestParseFilter(t *testing.T) {
	f, err := ParseFilter(`label:Bug label:Crash fix login`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if !strings.Contains(string(b), `"or":[{"labels"`) || !strings.Contains(string(b), `"containsIgnoreCase":"fix"`) {
		t.Fatalf("%s", b)
	}
	if _, err := ParseFilter("colour:red"); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := ParseFilter("priority:9"); err == nil {
		t.Fatal("bad priority accepted")
	}
	_ = url.Values{}
}

func hasField(fs []tracker.FieldValue, name, value string) bool {
	for _, f := range fs {
		if f.Name == name && f.Value == value {
			return true
		}
	}
	return false
}

// docsFake serves Linear's documents: three, two to a page, one in the Handbook project.
func docsFake(t *testing.T) (*httptest.Server, *[]map[string]any) {
	var seen []map[string]any
	var mu sync.Mutex
	doc := func(id, project string) map[string]any {
		d := map[string]any{"id": id, "title": "Doc " + id, "content": "# " + id + "\ncanary rollout", "url": "https://linear.app/acme/document/" + id, "updatedAt": "2026-01-02T03:04:05.000Z", "project": nil}
		if project != "" {
			d["project"] = map[string]any{"name": project}
		}
		return d
	}
	docs := []map[string]any{doc("d1", "Handbook"), doc("d2", ""), doc("d3", "Other")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen = append(seen, req.Variables)
		mu.Unlock()
		switch {
		case strings.Contains(req.Query, "searchDocuments("):
			write(w, map[string]any{"data": map[string]any{"searchDocuments": map[string]any{"nodes": docs}}})
		case strings.Contains(req.Query, "documents("):
			list := docs
			if f, ok := req.Variables["filter"].(map[string]any); ok {
				name := f["project"].(map[string]any)["name"].(map[string]any)["eqIgnoreCase"].(string)
				list = nil
				for _, d := range docs {
					if p, ok := d["project"].(map[string]any); ok && strings.EqualFold(p["name"].(string), name) {
						list = append(list, d)
					}
				}
			}
			start := 0
			if a, ok := req.Variables["after"].(string); ok {
				for i, d := range list {
					if d["id"] == a {
						start = i + 1
					}
				}
			}
			end := min(start+2, len(list))
			last := ""
			if end > 0 {
				last = list[end-1]["id"].(string)
			}
			write(w, map[string]any{"data": map[string]any{"documents": map[string]any{"nodes": list[start:end], "pageInfo": map[string]any{"hasNextPage": end < len(list), "endCursor": last}}}})
		case strings.Contains(req.Query, "document(id"):
			for _, d := range docs {
				if d["id"] == req.Variables["id"] {
					write(w, map[string]any{"data": map[string]any{"document": d}})
					return
				}
			}
			write(w, map[string]any{"errors": []any{map[string]any{"message": "Entity not found: Document", "extensions": map[string]any{"type": "invalid input"}}}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestDocumentsServeKnowledge(t *testing.T) {
	srv, _ := docsFake(t)
	pr, err := Open(tracker.OpenOptions{BaseURL: srv.URL, Auth: tracker.AuthToken, Credentials: tracker.Credentials{Secret: apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	p := pr.(*Provider)
	ctx := context.Background()
	if c := DocsCapabilities(); len(c.AuthMethods) != 0 || len(c.BorrowFrom) != 1 || c.BorrowFrom[0] != Name {
		t.Fatalf("Linear documents borrow a Linear sign-in: %+v", c)
	}
	hits, err := p.SearchArticles(ctx, "canary", "", 2)
	if err != nil || len(hits) != 2 || hits[0].ID != "d1" || hits[0].Project != "Handbook" || !strings.Contains(hits[0].Content, "canary") {
		t.Fatalf("%+v %v", hits, err)
	}
	if hits, err := p.SearchArticles(ctx, "canary", `project: "handbook"`, 5); err != nil || len(hits) != 1 || hits[0].ID != "d1" {
		t.Fatalf("the narrowing keeps one project's: %+v %v", hits, err)
	}
	// Three documents, two a page from Linear: the listing reads on through the cursor.
	list, more, err := p.ListArticles(ctx, "", "", tracker.Page{Limit: 2, Offset: 1})
	if err != nil || len(list) != 2 || list[0].ID != "d2" || list[1].ID != "d3" || more {
		t.Fatalf("%+v %v %v", list, more, err)
	}
	if list, _, err := p.ListArticles(ctx, "", "Handbook", tracker.Page{Limit: 10}); err != nil || len(list) != 1 || list[0].ID != "d1" {
		t.Fatalf("%+v %v", list, err)
	}
	if list, _, _ := p.ListArticles(ctx, "d1", "", tracker.Page{}); len(list) != 0 {
		t.Fatal("documents have no children")
	}
	a, err := p.GetArticle(ctx, "d2")
	if err != nil || a.Title != "Doc d2" || a.URL == "" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := p.GetArticle(ctx, "nope"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
