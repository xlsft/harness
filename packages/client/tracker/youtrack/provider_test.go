package youtrack_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/tracker"
	"github.com/xlsft/harness/packages/client/tracker/youtrack"
	"github.com/xlsft/harness/packages/client/tracker/youtrack/youtracktest"
)

// api is a fake YouTrack REST API over the fake Hub of youtracktest.
type api struct {
	mu            sync.Mutex
	regularUser   bool
	ignoreQuery   bool
	stage         string
	updateBodies  []string
	commentsTaken []string
	fail          []int
}

const stageField = `{"$type":"StateProjectCustomField","field":{"name":"Stage","fieldType":{"id":"state[1]","isMultiValue":false,"valueType":"state"}},"bundle":{"values":[{"name":"Backlog"},{"name":"Review"},{"name":"Old","archived":true}]}}`

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if len(a.fail) > 0 {
		code := a.fail[0]
		a.fail = a.fail[1:]
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":"fail","error_description":"failing"}`))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	write := func(s string) { _, _ = w.Write([]byte(s)) }
	switch {
	case path == "users/me":
		write(`{"login":"alice","fullName":"Alice A"}`)
	case path == "admin/projects/p1/customFields":
		if a.regularUser {
			w.WriteHeader(http.StatusForbidden)
			write(`{"error":"Forbidden"}`)
			return
		}
		write("[" + stageField + "]")
	case path == "admin/projects":
		write(`[{"id":"p1","shortName":"DEMO","name":"Demo","description":"About","iconUrl":"/icon.png"},{"id":"p2","shortName":"OLD","archived":true}]`)
	case path == "issues":
		if strings.Contains(r.URL.Query().Get("fields"), "projectCustomField") {
			write(`[{"customFields":[{"projectCustomField":` + stageField + `}]}]`)
			return
		}
		write(`[{"idReadable":"DEMO-1","summary":"One","project":{"shortName":"DEMO"}},{"idReadable":"DEMO-2","summary":"Two","project":{"shortName":"DEMO"}}]`)
	case path == "issues/DEMO-1":
		if r.Method == http.MethodPost {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			raw, _ := json.Marshal(b)
			a.updateBodies = append(a.updateBodies, string(raw))
			write(`{}`)
			return
		}
		write(`{"idReadable":"DEMO-1","summary":"One","description":"Body","updated":1700000000000,"project":{"id":"p1","shortName":"DEMO"},"customFields":[{"name":"Stage","value":{"name":"Backlog"}},{"name":"Assignee","value":[{"login":"bob"},{"fullName":"Carol C"}]}]}`)
	case path == "issues/DEMO-1/comments":
		if r.Method == http.MethodPost {
			var b struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			a.commentsTaken = append(a.commentsTaken, b.Text)
			write(`{}`)
			return
		}
		write(`[{"text":"live","created":1,"author":{"login":"bob"}},{"deleted":true}]`)
	case path == "groups":
		w.WriteHeader(http.StatusForbidden)
		write(`{"error":"Forbidden"}`)
	case path == "articles":
		write(`[{"idReadable":"H-1","summary":"Zebra","content":"stripes","project":{"shortName":"H"}},{"idReadable":"H-2","summary":"Lion","content":"mane","project":{"shortName":"H"},"parentArticle":{"idReadable":"H-1"}}]`)
	case path == "articles/H-1":
		write(`{"idReadable":"H-1","summary":"Zebra","content":"stripes","childArticles":[{"idReadable":"H-2"}]}`)
	case path == "articles/H-1/childArticles":
		write(`[{"idReadable":"H-2","summary":"Lion","content":"mane"}]`)
	default:
		w.WriteHeader(http.StatusNotFound)
		write(`{"error":"Not Found","error_description":"no such entity"}`)
	}
}

func open(t *testing.T, a *api) (*youtracktest.Server, tracker.Provider) {
	t.Helper()
	f := youtracktest.New(t, a)
	p, err := youtrack.Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthPassword, Login: youtracktest.Login, Credentials: tracker.Credentials{Secret: youtracktest.Password}})
	if err != nil {
		t.Fatal(err)
	}
	return f, p
}

func TestPasswordSignInAndRead(t *testing.T) {
	_, p := open(t, &api{})
	ctx := context.Background()
	me, err := p.Test(ctx)
	if err != nil || me.Login != "alice" {
		t.Fatalf("%v %v", me, err)
	}
	issues, more, err := p.Search(ctx, "x", tracker.Page{Limit: 1})
	if err != nil || len(issues) != 1 || !more || !strings.HasSuffix(issues[0].URL, "/issue/DEMO-1") {
		t.Fatalf("%v %v %v", issues, more, err)
	}
	i, err := p.Get(ctx, "DEMO-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(i.Comments) != 1 || i.Comments[0].Text != "live" || i.Comments[0].Author != "bob" {
		t.Fatalf("deleted comments stay out: %+v", i.Comments)
	}
	if i.Fields[1].Value != "bob, Carol C" {
		t.Fatalf("%+v", i.Fields)
	}
	if _, err := p.Get(ctx, "NOPE-1"); !errors.Is(err, tracker.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestBadPasswordIsAnAuthError(t *testing.T) {
	f := youtracktest.New(t, &api{})
	p, err := youtrack.Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthPassword, Login: "alice", Credentials: tracker.Credentials{Secret: "wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Test(context.Background())
	if !errors.Is(err, tracker.ErrAuth) {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("the password must not show: %v", err)
	}
}

func TestFieldsAndUpdateWithoutAdminRights(t *testing.T) {
	for _, regular := range []bool{false, true} {
		a := &api{regularUser: regular}
		_, p := open(t, a)
		ctx := context.Background()
		fields, err := p.Fields(ctx, "DEMO")
		if err != nil || len(fields) != 1 || fields[0].Name != "Stage" || strings.Join(fields[0].Values, ",") != "Backlog,Review" {
			t.Fatalf("regular=%v: %+v %v", regular, fields, err)
		}
		if err := p.Update(ctx, "DEMO-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Stage", Value: "Review"}}}); err != nil {
			t.Fatalf("regular=%v: %v", regular, err)
		}
		if !strings.Contains(a.updateBodies[0], `"$type":"StateIssueCustomField"`) || !strings.Contains(a.updateBodies[0], `"name":"Review"`) {
			t.Fatalf("%s", a.updateBodies[0])
		}
		if err := p.Update(ctx, "DEMO-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Stage", Value: "Nonsense"}}}); err == nil || !strings.Contains(err.Error(), "Backlog") {
			t.Fatalf("a bad value must say what is allowed: %v", err)
		}
		if err := p.Update(ctx, "DEMO-1", tracker.IssueUpdate{Fields: []tracker.FieldValue{{Name: "Nope", Value: "x"}}}); err == nil || !strings.Contains(err.Error(), "Stage") {
			t.Fatalf("an unknown field must say what exists: %v", err)
		}
	}
}

func TestGroupsFallBackToHubAndSayWhenTheyCant(t *testing.T) {
	f := youtracktest.New(t, &api{})
	f.HubAPI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usergroups":[{"id":"g1","name":"Team"}]}`))
	})
	p, err := youtrack.Open(tracker.OpenOptions{BaseURL: f.URL, Auth: tracker.AuthPassword, Login: youtracktest.Login, Credentials: tracker.Credentials{Secret: youtracktest.Password}})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := p.(*youtrack.Provider).Groups(context.Background())
	if err != nil || len(groups) != 1 || groups[0].Name != "Team" {
		t.Fatalf("%v %v", groups, err)
	}
	f.HubAPI = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usergroups":[]}`))
	})
	if _, err := p.(*youtrack.Provider).Groups(context.Background()); err == nil || !strings.Contains(err.Error(), "only admins") {
		t.Fatalf("%v", err)
	}
}

func TestProjectsAndKnowledge(t *testing.T) {
	_, p := open(t, &api{})
	ctx := context.Background()
	list, err := p.Projects(ctx)
	if err != nil || len(list) != 1 || list[0].Key != "DEMO" || !strings.HasSuffix(list[0].Logo, "/icon.png") {
		t.Fatalf("%+v %v", list, err)
	}
	k := p.(tracker.KnowledgeProvider)
	hits, err := k.SearchArticles(ctx, "zebra", "project: H", 5)
	if err != nil || len(hits) != 1 || hits[0].ID != "H-1" {
		t.Fatalf("what an instance gives back is filtered by the words too: %+v %v", hits, err)
	}
	top, _, err := k.ListArticles(ctx, "", "", tracker.Page{Limit: 10})
	if err != nil || len(top) != 1 || top[0].ID != "H-1" {
		t.Fatalf("%+v %v", top, err)
	}
	kids, _, err := k.ListArticles(ctx, "H-1", "", tracker.Page{Limit: 10})
	if err != nil || len(kids) != 1 || kids[0].ID != "H-2" {
		t.Fatalf("%+v %v", kids, err)
	}
	a, err := k.GetArticle(ctx, "H-1")
	if err != nil || !a.HasChildren || a.Content != "stripes" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestTransientErrorsAreClassified(t *testing.T) {
	a := &api{}
	_, p := open(t, a)
	// 500 is not retried by the client, and is one a later attempt may get past.
	a.fail = []int{500}
	_, err := p.Test(context.Background())
	if !tracker.IsTransient(err) {
		t.Fatalf("%v", err)
	}
	a.fail = []int{403}
	if _, err := p.Test(context.Background()); !errors.Is(err, tracker.ErrForbidden) {
		t.Fatalf("%v", err)
	}
}
