package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/tracker"
)

// fake is a Jira with Confluence and Atlassian's OAuth endpoints at one address. Gateway paths (/ex/jira/<id>, /ex/confluence/<id>)
// are served like the site's own.
type fake struct {
	*httptest.Server
	t        *testing.T
	mu       sync.Mutex
	status   map[string]string
	labels   map[string][]string
	fields   map[string]map[string]any
	comments map[string][]string
	created  []map[string]any
	searches []url.Values
	refresh  int
}

const (
	email    = "alice@example.test"
	apiToken = "api-token-1"
	pat      = "pat-1"
	cloudID  = "cloud-1"
)

func newFake(t *testing.T) *fake {
	f := &fake{t: t, status: map[string]string{"DEMO-1": "To Do", "DEMO-2": "In Progress", "DEMO-3": "To Do"}, labels: map[string][]string{"DEMO-1": {"a"}}, fields: map[string]map[string]any{}, comments: map[string][]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+apiToken))
	return h == basic || h == "Bearer "+pat || h == "Bearer access-1" || h == "Bearer access-2"
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func issueJSON(key, status string, labels []string) map[string]any {
	return map[string]any{"key": key, "fields": map[string]any{
		"summary": "Issue " + key, "description": "Body of " + key, "updated": "2026-01-02T03:04:05.000+0000", "project": map[string]any{"key": "DEMO"},
		"status": map[string]any{"name": status}, "priority": map[string]any{"name": "High"}, "issuetype": map[string]any{"name": "Task"},
		"assignee": map[string]any{"displayName": "Alice"}, "labels": labels,
		"comment": map[string]any{"comments": []any{map[string]any{"author": map[string]any{"displayName": "Bob"}, "body": "first", "created": "2026-01-02T03:04:05.000+0000"}}},
	}}
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/oauth/token" && r.Method == http.MethodPost {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch body["grant_type"] {
		case "authorization_code":
			if body["code"] != "code-1" || body["code_verifier"] == "" {
				w.WriteHeader(http.StatusBadRequest)
				write(w, map[string]string{"error": "invalid_grant"})
				return
			}
			write(w, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600})
		case "refresh_token":
			f.refresh++
			write(w, map[string]any{"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 3600})
		}
		return
	}
	if !f.authorized(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if path == "/oauth/token/accessible-resources" {
		write(w, []any{map[string]any{"id": "other", "url": "https://other.atlassian.net"}, map[string]any{"id": cloudID, "url": f.URL}})
		return
	}
	for _, prefix := range []string{"/ex/jira/" + cloudID, "/ex/confluence/" + cloudID} {
		path = strings.TrimPrefix(path, prefix)
	}
	if strings.HasPrefix(path, "/wiki/rest/api/") {
		f.wiki(w, r, strings.TrimPrefix(path, "/wiki/rest/api/"))
		return
	}
	if strings.HasPrefix(path, "/confluence/rest/api/") {
		f.wiki(w, r, strings.TrimPrefix(path, "/confluence/rest/api/"))
		return
	}
	rest := strings.TrimPrefix(path, "/rest/api/2/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case rest == "myself":
		write(w, map[string]any{"accountId": "acc-alice", "name": "alice", "emailAddress": email, "displayName": "Alice"})
	case rest == "search/jql" || rest == "search":
		f.searches = append(f.searches, r.URL.Query())
		var keys []string
		for _, k := range []string{"DEMO-1", "DEMO-2", "DEMO-3"} {
			if strings.Contains(r.URL.Query().Get("jql"), "To Do") && f.status[k] != "To Do" {
				continue
			}
			keys = append(keys, k)
		}
		if rest == "search" {
			start, _ := strconv.Atoi(r.URL.Query().Get("startAt"))
			max, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
			var out []any
			for i := start; i < len(keys) && i < start+max; i++ {
				out = append(out, issueJSON(keys[i], f.status[keys[i]], nil))
			}
			write(w, map[string]any{"issues": out, "total": len(keys)})
			return
		}
		// One issue a page, to exercise nextPageToken.
		at, _ := strconv.Atoi(r.URL.Query().Get("nextPageToken"))
		if at >= len(keys) {
			write(w, map[string]any{"issues": []any{}, "isLast": true})
			return
		}
		res := map[string]any{"issues": []any{issueJSON(keys[at], f.status[keys[at]], nil)}}
		if at+1 < len(keys) {
			res["nextPageToken"] = strconv.Itoa(at + 1)
		} else {
			res["isLast"] = true
		}
		write(w, res)
	case rest == "field":
		write(w, []any{
			map[string]any{"id": "summary", "name": "Summary", "schema": map[string]any{"type": "string", "system": "summary"}},
			map[string]any{"id": "priority", "name": "Priority", "schema": map[string]any{"type": "priority", "system": "priority"}},
			map[string]any{"id": "assignee", "name": "Assignee", "schema": map[string]any{"type": "user", "system": "assignee"}},
			map[string]any{"id": "labels", "name": "Labels", "schema": map[string]any{"type": "array", "items": "string", "system": "labels"}},
			map[string]any{"id": "customfield_1", "name": "Points", "custom": true, "schema": map[string]any{"type": "number"}},
			map[string]any{"id": "customfield_2", "name": "Team", "custom": true, "schema": map[string]any{"type": "option"}},
		})
	case rest == "priority":
		write(w, []any{map[string]any{"name": "High"}, map[string]any{"name": "Low"}})
	case rest == "project/DEMO/statuses":
		write(w, []any{map[string]any{"statuses": []any{map[string]any{"name": "To Do"}, map[string]any{"name": "In Progress"}, map[string]any{"name": "Done"}}}})
	case rest == "project/search":
		write(w, map[string]any{"values": []any{map[string]any{"key": "DEMO", "name": "Demo", "avatarUrls": map[string]string{"48x48": "https://x/a.png"}}}, "isLast": true})
	case rest == "project":
		write(w, []any{map[string]any{"key": "DEMO", "name": "Demo"}, map[string]any{"key": "OLD", "name": "Old", "archived": true}})
	case rest == "user/search":
		write(w, []any{map[string]any{"accountId": "acc-bob", "displayName": "Bob", "emailAddress": "bob@example.test"}})
	case rest == "issue" && r.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		f.status["DEMO-9"] = "To Do"
		write(w, map[string]any{"key": "DEMO-9"})
	case strings.HasPrefix(rest, "issue/"):
		parts := strings.Split(strings.TrimPrefix(rest, "issue/"), "/")
		key := parts[0]
		if _, ok := f.status[key]; !ok {
			w.WriteHeader(http.StatusNotFound)
			write(w, map[string]any{"errorMessages": []string{"Issue does not exist or you do not have permission to see it."}})
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			write(w, issueJSON(key, f.status[key], f.labels[key]))
		case len(parts) == 1 && r.Method == http.MethodPut:
			var body struct {
				Fields map[string]any                 `json:"fields"`
				Update map[string][]map[string]string `json:"update"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.fields[key] == nil {
				f.fields[key] = map[string]any{}
			}
			for k, v := range body.Fields {
				f.fields[key][k] = v
			}
			for _, op := range body.Update["labels"] {
				if v, ok := op["add"]; ok {
					f.labels[key] = append(f.labels[key], v)
				}
				if v, ok := op["remove"]; ok {
					var keep []string
					for _, l := range f.labels[key] {
						if l != v {
							keep = append(keep, l)
						}
					}
					f.labels[key] = keep
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case parts[1] == "transitions" && r.Method == http.MethodGet:
			write(w, map[string]any{"transitions": []any{map[string]any{"id": "11", "name": "Start", "to": map[string]any{"name": "In Progress"}}, map[string]any{"id": "31", "name": "Finish", "to": map[string]any{"name": "Done"}}}})
		case parts[1] == "transitions":
			var body struct {
				Transition struct{ ID string } `json:"transition"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.status[key] = map[string]string{"11": "In Progress", "31": "Done"}[body.Transition.ID]
			w.WriteHeader(http.StatusNoContent)
		case parts[1] == "comment":
			var body struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.comments[key] = append(f.comments[key], body.Body)
			write(w, map[string]any{"id": "c1"})
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func page(id, title string, parent string, children int) map[string]any {
	p := map[string]any{"id": id, "title": title, "space": map[string]any{"key": "HELP"}, "version": map[string]any{"when": "2026-01-02T03:04:05.000Z"},
		"body":     map[string]any{"storage": map[string]any{"value": "<h1>" + title + "</h1><p>Deploy with <b>care</b> &amp; patience.</p><ul><li>one</li></ul>"}},
		"children": map[string]any{"page": map[string]any{"size": children}}, "_links": map[string]any{"webui": "/spaces/HELP/pages/" + id}}
	if parent != "" {
		p["ancestors"] = []any{map[string]any{"id": parent}}
	}
	return p
}

func (f *fake) wiki(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case rest == "content/search":
		cql := r.URL.Query().Get("cql")
		if strings.Contains(cql, "parent = ") {
			write(w, map[string]any{"results": []any{page("2", "Child", "1", 0)}})
			return
		}
		write(w, map[string]any{"results": []any{page("1", "Deploy guide", "", 1), page("3", "Other", "", 0)}, "cql": cql})
	case rest == "content/1":
		write(w, page("1", "Deploy guide", "", 1))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func cloud(t *testing.T) {
	old := IsCloud
	IsCloud = func(*url.URL) bool { return true }
	t.Cleanup(func() { IsCloud = old })
}

func openToken(t *testing.T, f *fake) *Provider {
	p, err := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthEmailToken, Login: email, Credentials: tracker.Credentials{Secret: apiToken}})
	if err != nil {
		t.Fatal(err)
	}
	return p.(*Provider)
}

func TestCloudAPIToken(t *testing.T) {
	cloud(t)
	f := newFake(t)
	p := openToken(t, f)
	ctx := context.Background()
	me, err := p.Test(ctx)
	if err != nil || me.Login != email || me.DisplayName != "Alice" {
		t.Fatalf("test: %+v %v", me, err)
	}
	// Cloud's search/jql pages with nextPageToken; one issue a page here.
	issues, more, err := p.Search(ctx, `project = DEMO AND status = "To Do"`, tracker.Page{Limit: 5})
	if err != nil || more || len(issues) != 2 || issues[0].ID != "DEMO-1" || issues[1].ID != "DEMO-3" || issues[0].URL != f.URL+"/browse/DEMO-1" {
		t.Fatalf("search: %+v %v %v", issues, more, err)
	}
	issues, more, err = p.Search(ctx, "project = DEMO", tracker.Page{Limit: 1, Offset: 1})
	if err != nil || !more || len(issues) != 1 || issues[0].ID != "DEMO-2" {
		t.Fatalf("offset: %+v %v %v", issues, more, err)
	}
	got, err := p.Get(ctx, "DEMO-1")
	if err != nil || got.Description != "Body of DEMO-1" || len(got.Comments) != 1 || got.Comments[0].Author != "Bob" || got.UpdatedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := p.Get(ctx, "DEMO-404"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// Status goes through a transition, labels are added and removed, other fields are shaped by their schema.
	err = p.Update(ctx, "DEMO-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Status", Value: "in progress"}, {Name: "Add labels", Value: "review, b"}, {Name: "Remove labels", Value: "a"}, {Name: "Points", Value: "3"}, {Name: "Team", Value: "Core"}, {Name: "Priority", Value: "Low"}, {Name: "Assignee", Value: "bob@example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if f.status["DEMO-1"] != "In Progress" || strings.Join(f.labels["DEMO-1"], ",") != "review,b" {
		t.Fatalf("after update: %v %v", f.status["DEMO-1"], f.labels["DEMO-1"])
	}
	fs := f.fields["DEMO-1"]
	if fs["customfield_1"] != 3.0 || fs["customfield_2"].(map[string]any)["value"] != "Core" || fs["priority"].(map[string]any)["name"] != "Low" || fs["assignee"].(map[string]any)["accountId"] != "acc-bob" {
		t.Fatalf("fields: %v", fs)
	}
	if err := p.Update(ctx, "DEMO-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Status", Value: "Nowhere"}}}); err == nil || !strings.Contains(err.Error(), "In Progress, Done") {
		t.Fatalf("bad transition: %v", err)
	}
	if err := p.Update(ctx, "DEMO-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Nope", Value: "x"}}}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := p.Comment(ctx, "DEMO-1", "hello"); err != nil || f.comments["DEMO-1"][0] != "hello" {
		t.Fatalf("comment: %v %v", err, f.comments)
	}
	created, err := p.Create(ctx, tracker.IssueDraft{Project: "DEMO", Title: "New", Fields: []tracker.FieldValue{{Name: "Type", Value: "Bug"}}})
	if err != nil || created.ID != "DEMO-9" || f.created[0]["fields"].(map[string]any)["issuetype"].(map[string]any)["name"] != "Bug" {
		t.Fatalf("create: %+v %v %v", created, err, f.created)
	}
	fields, err := p.Fields(ctx, "DEMO")
	if err != nil || fields[0].Name != "Status" || strings.Join(fields[0].Values, ",") != "To Do,In Progress,Done" || fields[len(fields)-1].Name != "Team" {
		t.Fatalf("fields: %+v %v", fields, err)
	}
	projects, err := p.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Key != "DEMO" || projects[0].Logo != "https://x/a.png" {
		t.Fatalf("projects: %+v %v", projects, err)
	}

	// Confluence under the site.
	arts, err := p.SearchArticles(ctx, "deploy", "space = HELP", 5)
	if err != nil || len(arts) != 2 || arts[0].Title != "Deploy guide" || !strings.Contains(arts[0].Content, "Deploy with care & patience.") || arts[0].URL != f.URL+"/wiki/spaces/HELP/pages/1" || arts[0].Project != "HELP" {
		t.Fatalf("articles: %+v %v", arts, err)
	}
	top, _, err := p.ListArticles(ctx, "", "space = HELP", tracker.Page{Limit: 10})
	if err != nil || len(top) != 2 || !top[0].HasChildren {
		t.Fatalf("list: %+v %v", top, err)
	}
	kids, _, err := p.ListArticles(ctx, "1", "", tracker.Page{Limit: 10})
	if err != nil || len(kids) != 1 || kids[0].ParentID != "1" {
		t.Fatalf("children: %+v %v", kids, err)
	}
	a, err := p.GetArticle(ctx, "1")
	if err != nil || a.Title != "Deploy guide" || !strings.Contains(a.Content, "- one") {
		t.Fatalf("article: %+v %v", a, err)
	}

	bad, _ := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthEmailToken, Login: email, Credentials: tracker.Credentials{Secret: "wrong"}})
	if _, err := bad.Test(ctx); !errors.Is(err, tracker.ErrAuth) {
		t.Fatalf("wrong token: %v", err)
	}
}

func TestDataCenterPAT(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	p, err := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthToken, Credentials: tracker.Credentials{Secret: pat}})
	if err != nil {
		t.Fatal(err)
	}
	me, err := p.Test(ctx)
	if err != nil || me.Login != email {
		t.Fatalf("test: %+v %v", me, err)
	}
	issues, more, err := p.Search(ctx, "project = DEMO", tracker.Page{Limit: 2})
	if err != nil || !more || len(issues) != 2 || f.searches[0].Get("startAt") != "0" {
		t.Fatalf("search: %+v %v %v", issues, more, err)
	}
	// Data Center names users by name, and has no Confluence under the site.
	if err := p.Update(ctx, "DEMO-2", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Assignee", Value: "me"}}}); err != nil || f.fields["DEMO-2"]["assignee"].(map[string]any)["name"] != "alice" {
		t.Fatalf("assignee: %v %v", err, f.fields)
	}
	// A Confluence connection borrowing this sign-in needs Confluence's own address on Data Center.
	borrowed, err := OpenKnowledge(tracker.KnowledgeOpenOptions{BaseURL: f.URL, TrackerBaseURL: f.URL, BorrowedFrom: Name, Auth: tracker.AuthToken, Credentials: tracker.Credentials{Secret: pat}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := borrowed.SearchArticles(ctx, "x", "", 3); err == nil || !strings.Contains(err.Error(), "Confluence address") {
		t.Fatalf("no confluence: %v", err)
	}
	projects, err := p.Projects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects: %+v %v", projects, err)
	}
	withWiki, err := OpenKnowledge(tracker.KnowledgeOpenOptions{BaseURL: f.URL + "/confluence", TrackerBaseURL: f.URL, BorrowedFrom: Name, Auth: tracker.AuthToken, Credentials: tracker.Credentials{Secret: pat}})
	if err != nil {
		t.Fatal(err)
	}
	arts, err := withWiki.SearchArticles(ctx, "deploy", "", 3)
	if err != nil || len(arts) != 2 || arts[0].URL != f.URL+"/confluence/spaces/HELP/pages/1" {
		t.Fatalf("confluence dc: %+v %v", arts, err)
	}
}

func TestCloudOAuth(t *testing.T) {
	cloud(t)
	f := newFake(t)
	oldAuth, oldGateway := AuthURL, GatewayURL
	AuthURL, GatewayURL = f.URL, f.URL
	t.Cleanup(func() { AuthURL, GatewayURL = oldAuth, oldGateway })
	ctx := context.Background()
	fac := tracker.Factories()
	var ex tracker.OAuthExchange
	for _, x := range fac {
		if x.Capabilities.Provider == Name {
			ex, _ = x.OAuth(f.URL)
		}
	}
	u, err := ex.AuthorizeURL(ctx, "cli", "http://127.0.0.1:1/callback", "st", "ch")
	if err != nil || !strings.HasPrefix(u, f.URL+"/authorize?") || !strings.Contains(u, "audience=api.atlassian.com") || !strings.Contains(u, "offline_access") {
		t.Fatalf("authorize: %s %v", u, err)
	}
	creds, err := ex.Exchange(ctx, "cli", "sec", "code-1", "http://127.0.0.1:1/callback", "ver")
	if err != nil || creds.AccessToken != "access-1" || creds.RefreshToken != "refresh-1" {
		t.Fatalf("exchange: %+v %v", creds, err)
	}
	if _, err := ex.Exchange(ctx, "cli", "sec", "bad", "http://127.0.0.1:1/callback", "ver"); !errors.Is(err, tracker.ErrAuth) {
		t.Fatalf("bad code: %v", err)
	}
	// An expired token is refreshed and stored; requests go through the gateway under the site's cloud id.
	creds.ExpiresAt = 1
	var stored tracker.Credentials
	p, err := Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthOAuth, OAuthClientID: "cli", Credentials: creds, OnTokens: func(c tracker.Credentials) { stored = c }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "access-2" || stored.RefreshToken != "refresh-2" || f.refresh != 1 {
		t.Fatalf("refresh: %+v %d", stored, f.refresh)
	}
	// Confluence borrowing the OAuth sign-in goes through the gateway too.
	wiki, err := OpenKnowledge(tracker.KnowledgeOpenOptions{BaseURL: f.URL, TrackerBaseURL: f.URL, BorrowedFrom: Name, Auth: tracker.AuthOAuth, OAuthClientID: "cli", Credentials: stored, OnTokens: func(c tracker.Credentials) { stored = c }})
	if err != nil {
		t.Fatal(err)
	}
	arts, err := wiki.SearchArticles(ctx, "deploy", "", 3)
	if err != nil || len(arts) != 2 {
		t.Fatalf("confluence through the gateway: %v %v", arts, err)
	}
}

func TestCQL(t *testing.T) {
	if got := CQL(`deploy "now"`, "space = HELP"); got != `type = page AND (space = HELP) AND text ~ "deploy \"now\""` {
		t.Fatal(got)
	}
	if got := CQL(`title ~ "x" and space = A`, ""); got != `type = page AND (title ~ "x" and space = A)` {
		t.Fatal(got)
	}
	_ = io.EOF
}
