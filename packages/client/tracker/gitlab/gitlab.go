// Package gitlab is GitLab Issues as a tracker. Like GitHub Issues it signs in through the code host (AuthVCS): on a device with
// the token of the glab login for the host (OpenOptions.VCSToken), on the server (packages/core/src/tracker/gitlab.ts) through a
// connection from Settings > VCS. REST API: https://docs.gitlab.com/api/issues/, https://docs.gitlab.com/api/notes/.
// docs/design/tracker.md, "Writing a provider".
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/tracker"
)

// Name is this provider's name in a connection.
const Name = "gitlab"

func init() {
	tracker.Register(Name, tracker.Factory{Capabilities: Capabilities(), Open: Open})
}

// Capabilities describes the provider.
func Capabilities() tracker.Capabilities {
	return tracker.Capabilities{
		Provider: Name, DisplayName: "GitLab Issues",
		AuthMethods:   []tracker.AuthMethod{tracker.AuthVCS},
		QueryLanguage: "GitLab filters", QueryExample: `project:group/app state:opened label:bug assignee:me login page`,
		CustomFields: false, Comments: true, DefaultBaseURL: "https://gitlab.com",
		OptionFields:       []tracker.OptionField{vcsOptionFields[0]},
		DocsURL:            "https://docs.gitlab.com/api/issues/",
		Logo:               "gitlab",
		BaseURLPlaceholder: "https://gitlab.com",
		AuthHints: map[tracker.AuthMethod]tracker.AuthHint{
			tracker.AuthVCS: {Help: "Signs in through GitLab: on this device the glab command line (glab auth login), for the company a connection from Settings → VCS."},
		},
	}
}

var vcsOptionFields = []tracker.OptionField{
	{Key: "vcs_connection_id", Label: "Code host connection", Help: "The id of a connection from Settings > VCS. On a device, gh or glab signs in instead."},
}

// Provider implements tracker.Provider.
type Provider struct {
	api *tracker.JSONClient

	mu sync.Mutex
	me *user
}

type user struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

var _ tracker.Provider = (*Provider)(nil)

// Open makes a provider for a connection.
func Open(o tracker.OpenOptions) (tracker.Provider, error) {
	if o.Auth != tracker.AuthVCS {
		return nil, errors.New("GitLab Issues signs in through the code host")
	}
	if o.VCSToken == nil {
		return nil, fmt.Errorf("%w: no code host sign-in is available here", tracker.ErrAuth)
	}
	base := tracker.NormalizeURL(o.BaseURL)
	if base == "" {
		base = "https://gitlab.com"
	}
	host := "gitlab.com"
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Host
	}
	var (
		mu    sync.Mutex
		token string
	)
	c := &tracker.JSONClient{
		Base: strings.TrimRight(base, "/") + "/api/v4", HTTP: tracker.NewHTTPClient(), Name: "GitLab",
		Authorize: func(ctx context.Context, h http.Header) error {
			mu.Lock()
			defer mu.Unlock()
			if token == "" {
				t, err := o.VCSToken(ctx, host)
				if err != nil {
					return err
				}
				token = t
			}
			h.Set("Authorization", "Bearer "+token)
			return nil
		},
	}
	return &Provider{api: c}, nil
}

// Capabilities implements tracker.Provider.
func (*Provider) Capabilities() tracker.Capabilities { return Capabilities() }

// ParseID splits group/project#12 (groups nest, so the last # ends the path).
func ParseID(id string) (project string, iid int, err error) {
	id = strings.TrimSpace(id)
	i := strings.LastIndex(id, "#")
	if i > 0 {
		project = id[:i]
		iid, err = strconv.Atoi(id[i+1:])
	}
	if i <= 0 || err != nil || iid <= 0 || !strings.Contains(project, "/") || strings.ContainsAny(project, " \t") {
		return "", 0, fmt.Errorf("%w: %q is not group/project#number", tracker.ErrNotFound, id)
	}
	return project, iid, nil
}

// projectPath is the API path of a project by its full path.
func projectPath(project string) string { return "projects/" + url.PathEscape(project) }

// Test implements tracker.Provider.
func (p *Provider) Test(ctx context.Context) (tracker.Identity, error) {
	me, err := p.user(ctx)
	if err != nil {
		return tracker.Identity{}, err
	}
	return tracker.Identity{Login: me.Username, DisplayName: me.Name}, nil
}

func (p *Provider) user(ctx context.Context) (*user, error) {
	p.mu.Lock()
	me := p.me
	p.mu.Unlock()
	if me != nil {
		return me, nil
	}
	var u user
	if err := p.api.Do(ctx, http.MethodGet, "user", nil, nil, &u); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.me = &u
	p.mu.Unlock()
	return &u, nil
}

type apiIssue struct {
	IID         int      `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	WebURL      string   `json:"web_url"`
	State       string   `json:"state"`
	UpdatedAt   string   `json:"updated_at"`
	Labels      []string `json:"labels"`
	Assignees   []user   `json:"assignees"`
	Milestone   *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	References struct {
		Full string `json:"full"`
	} `json:"references"`
}

// projectOf reads group/project from an issue's full reference ("group/project#12").
func (i apiIssue) projectOf() string {
	if j := strings.LastIndex(i.References.Full, "#"); j > 0 {
		return i.References.Full[:j]
	}
	return ""
}

func (i apiIssue) toIssue(project string) tracker.Issue {
	if project == "" {
		project = i.projectOf()
	}
	out := tracker.Issue{ID: project + "#" + strconv.Itoa(i.IID), Title: i.Title, URL: i.WebURL, Description: i.Description, Project: project, UpdatedAt: i.UpdatedAt}
	add := func(name, v string) {
		if v != "" {
			out.Fields = append(out.Fields, tracker.FieldValue{Name: name, Value: v})
		}
	}
	add("State", i.State)
	add("Labels", strings.Join(i.Labels, ", "))
	var names []string
	for _, a := range i.Assignees {
		names = append(names, a.Username)
	}
	add("Assignees", strings.Join(names, ", "))
	if i.Milestone != nil {
		add("Milestone", i.Milestone.Title)
	}
	return out
}

// Filter is a parsed query: where to look and the list API's parameters.
type Filter struct {
	// Path is the list endpoint: "issues", "projects/<p>/issues" or "groups/<g>/issues".
	Path   string
	Params url.Values
}

// ParseQuery reads GitLab filters: project:, group:, state:, label: (repeatable), assignee:, author:, milestone:, scope:,
// and free text, which searches titles and descriptions. Values may be quoted. "me" as assignee or author is the signed-in
// account. Without project: or group: the search covers the issues the account can see (scope all).
func ParseQuery(q string) (Filter, error) {
	f := Filter{Path: "issues", Params: url.Values{}}
	var text, labels []string
	for _, tok := range tokens(q) {
		key, val, ok := strings.Cut(tok, ":")
		if !ok || val == "" || strings.ContainsAny(key, " \"") {
			text = append(text, strings.Trim(tok, `"`))
			continue
		}
		val = strings.Trim(val, `"`)
		switch strings.ToLower(key) {
		case "project", "repo":
			f.Path = projectPath(val) + "/issues"
		case "group":
			f.Path = "groups/" + url.PathEscape(val) + "/issues"
		case "state", "is":
			switch strings.ToLower(val) {
			case "open", "opened":
				f.Params.Set("state", "opened")
			case "closed":
				f.Params.Set("state", "closed")
			case "all":
			default:
				return f, fmt.Errorf("state:%s: GitLab issues are opened or closed", val)
			}
		case "label", "labels":
			labels = append(labels, val)
		case "assignee":
			f.Params.Set("assignee_username", val)
		case "author":
			f.Params.Set("author_username", val)
		case "milestone":
			f.Params.Set("milestone", val)
		case "scope":
			f.Params.Set("scope", val)
		default:
			return f, fmt.Errorf("unknown filter %q: GitLab filters are project, group, state, label, assignee, author, milestone and scope", key)
		}
	}
	if len(labels) > 0 {
		f.Params.Set("labels", strings.Join(labels, ","))
	}
	if s := strings.Join(text, " "); strings.TrimSpace(s) != "" {
		f.Params.Set("search", strings.TrimSpace(s))
	}
	if f.Path == "issues" && f.Params.Get("scope") == "" {
		f.Params.Set("scope", "all")
	}
	return f, nil
}

// tokens splits on spaces outside quotes.
func tokens(q string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range q {
		switch {
		case r == '"':
			quoted = !quoted
			b.WriteRune(r)
		case (r == ' ' || r == '\t') && !quoted:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// Search implements tracker.Provider, with GitLab filters.
func (p *Provider) Search(ctx context.Context, query string, page tracker.Page) ([]tracker.Issue, bool, error) {
	f, err := ParseQuery(query)
	if err != nil {
		return nil, false, err
	}
	for _, k := range []string{"assignee_username", "author_username"} {
		if strings.EqualFold(f.Params.Get(k), "me") {
			me, err := p.user(ctx)
			if err != nil {
				return nil, false, err
			}
			f.Params.Set(k, me.Username)
		}
	}
	limit := max(page.Limit, 1)
	const per = 100
	n := page.Offset/per + 1
	skip := page.Offset % per
	var out []tracker.Issue
	for ; n <= 10; n++ {
		q := url.Values{}
		for k, v := range f.Params {
			q[k] = v
		}
		q.Set("per_page", strconv.Itoa(per))
		q.Set("page", strconv.Itoa(n))
		q.Set("order_by", "updated_at")
		var list []apiIssue
		if err := p.api.Do(ctx, http.MethodGet, f.Path, q, nil, &list); err != nil {
			return nil, false, err
		}
		for _, i := range list {
			if skip > 0 {
				skip--
				continue
			}
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, i.toIssue(""))
		}
		if len(list) < per {
			return out, false, nil
		}
	}
	return out, false, nil
}

// Get implements tracker.Provider.
func (p *Provider) Get(ctx context.Context, id string) (*tracker.Issue, error) {
	project, iid, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	path := projectPath(project) + "/issues/" + strconv.Itoa(iid)
	var raw apiIssue
	if err := p.api.Do(ctx, http.MethodGet, path, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := raw.toIssue(project)
	var notes []struct {
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		System    bool   `json:"system"`
		Author    user   `json:"author"`
	}
	q := url.Values{"per_page": {"100"}, "sort": {"desc"}, "order_by": {"created_at"}}
	if err := p.api.Do(ctx, http.MethodGet, path+"/notes", q, nil, &notes); err != nil {
		return nil, err
	}
	// The newest ten that people wrote, oldest first.
	for j := len(notes) - 1; j >= 0; j-- {
		if c := notes[j]; !c.System {
			out.Comments = append(out.Comments, tracker.IssueComment{Author: c.Author.Username, Text: c.Body, CreatedAt: c.CreatedAt})
		}
	}
	if k := len(out.Comments); k > 10 {
		out.Comments = out.Comments[k-10:]
	}
	return &out, nil
}

// Create implements tracker.Provider. The project is its full path.
func (p *Provider) Create(ctx context.Context, d tracker.IssueDraft) (*tracker.Issue, error) {
	if !strings.Contains(d.Project, "/") {
		return nil, fmt.Errorf("%q is not group/project", d.Project)
	}
	var raw apiIssue
	if err := p.api.Do(ctx, http.MethodPost, projectPath(d.Project)+"/issues", nil, map[string]any{"title": d.Title, "description": d.Description}, &raw); err != nil {
		return nil, err
	}
	out := raw.toIssue(d.Project)
	if len(d.Fields) > 0 {
		if err := p.Update(ctx, out.ID, tracker.IssueUpdate{Fields: d.Fields}); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// userID is the id of a username; "me" is the signed-in account.
func (p *Provider) userID(ctx context.Context, name string) (int, error) {
	if strings.EqualFold(name, "me") {
		me, err := p.user(ctx)
		if err != nil {
			return 0, err
		}
		return me.ID, nil
	}
	var list []user
	if err := p.api.Do(ctx, http.MethodGet, "users", url.Values{"username": {name}}, nil, &list); err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, fmt.Errorf("no GitLab user %q", name)
	}
	return list[0].ID, nil
}

// Update implements tracker.Provider: State (opened, closed), Labels, Add labels, Remove labels, Assignees ("me" is the
// signed-in account, "none" nobody), Milestone, Title and Description.
func (p *Provider) Update(ctx context.Context, id string, u tracker.IssueUpdate) error {
	project, iid, err := ParseID(id)
	if err != nil {
		return err
	}
	body := map[string]any{}
	if u.Title != nil {
		body["title"] = *u.Title
	}
	if u.Description != nil {
		body["description"] = *u.Description
	}
	for _, f := range u.Fields {
		switch strings.ToLower(strings.TrimSpace(f.Name)) {
		case "state", "status", "stage":
			switch strings.ToLower(strings.TrimSpace(f.Value)) {
			case "open", "opened", "reopened", "reopen":
				body["state_event"] = "reopen"
			case "closed", "close", "done", "completed":
				body["state_event"] = "close"
			default:
				return fmt.Errorf("%q is not a state of a GitLab issue: opened or closed", f.Value)
			}
		case "labels":
			body["labels"] = strings.Join(splitList(f.Value), ",")
		case "add labels":
			body["add_labels"] = strings.Join(splitList(f.Value), ",")
		case "remove labels":
			body["remove_labels"] = strings.Join(splitList(f.Value), ",")
		case "assignees", "assignee":
			ids := []int{}
			for _, a := range splitList(f.Value) {
				if strings.EqualFold(a, "none") {
					continue
				}
				id, err := p.userID(ctx, a)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			if len(ids) == 0 {
				ids = []int{0}
			}
			body["assignee_ids"] = ids
		case "milestone":
			m, err := p.milestone(ctx, project, f.Value)
			if err != nil {
				return err
			}
			body["milestone_id"] = m
		case "title":
			body["title"] = f.Value
		case "description", "body":
			body["description"] = f.Value
		default:
			return fmt.Errorf("unknown field %q: GitLab Issues sets State, Labels, Add labels, Remove labels, Assignees, Milestone, Title and Description", f.Name)
		}
	}
	if len(body) == 0 {
		return nil
	}
	return p.api.Do(ctx, http.MethodPut, projectPath(project)+"/issues/"+strconv.Itoa(iid), nil, body, nil)
}

type milestone struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

func (p *Provider) milestones(ctx context.Context, project string) ([]milestone, error) {
	var list []milestone
	err := p.api.Do(ctx, http.MethodGet, projectPath(project)+"/milestones", url.Values{"per_page": {"100"}}, nil, &list)
	return list, err
}

// milestone is a milestone's id by title; 0 takes it off.
func (p *Provider) milestone(ctx context.Context, project, title string) (int, error) {
	t := strings.TrimSpace(title)
	if t == "" || strings.EqualFold(t, "none") {
		return 0, nil
	}
	list, err := p.milestones(ctx, project)
	if err != nil {
		return 0, err
	}
	for _, m := range list {
		if strings.EqualFold(m.Title, t) {
			return m.ID, nil
		}
	}
	return 0, fmt.Errorf("no milestone %q in %s", title, project)
}

// Comment implements tracker.Provider.
func (p *Provider) Comment(ctx context.Context, id, body string) error {
	project, iid, err := ParseID(id)
	if err != nil {
		return err
	}
	return p.api.Do(ctx, http.MethodPost, projectPath(project)+"/issues/"+strconv.Itoa(iid)+"/notes", nil, map[string]any{"body": body}, nil)
}

// Fields implements tracker.Provider: the project is its full path.
func (p *Provider) Fields(ctx context.Context, project string) ([]tracker.Field, error) {
	labels := tracker.Field{Name: "Labels", Kind: "labels", Multi: true}
	assignees := tracker.Field{Name: "Assignees", Kind: "user", Multi: true}
	ms := tracker.Field{Name: "Milestone", Kind: "bundle"}
	if project != "" {
		var ls []struct {
			Name string `json:"name"`
		}
		if err := p.api.Do(ctx, http.MethodGet, projectPath(project)+"/labels", url.Values{"per_page": {"100"}}, nil, &ls); err != nil {
			return nil, err
		}
		for _, l := range ls {
			labels.Values = append(labels.Values, l.Name)
		}
		var members []user
		if err := p.api.Do(ctx, http.MethodGet, projectPath(project)+"/members/all", url.Values{"per_page": {"100"}}, nil, &members); err == nil {
			for _, m := range members {
				assignees.Values = append(assignees.Values, m.Username)
			}
		}
		if list, err := p.milestones(ctx, project); err == nil {
			for _, m := range list {
				ms.Values = append(ms.Values, m.Title)
			}
		}
	}
	add, rm := labels, labels
	add.Name, rm.Name = "Add labels", "Remove labels"
	return []tracker.Field{{Name: "State", Kind: "state", Values: []string{"opened", "closed"}}, labels, add, rm, assignees, ms}, nil
}

// Projects implements tracker.Provider: the projects the account is a member of with issues on, most recently active first.
func (p *Provider) Projects(ctx context.Context) ([]tracker.Project, error) {
	var out []tracker.Project
	for n := 1; n <= 3; n++ {
		var list []struct {
			Path        string `json:"path_with_namespace"`
			Name        string `json:"name"`
			Description string `json:"description"`
			AvatarURL   string `json:"avatar_url"`
		}
		q := url.Values{"membership": {"true"}, "archived": {"false"}, "with_issues_enabled": {"true"}, "order_by": {"last_activity_at"},
			"simple": {"true"}, "per_page": {"100"}, "page": {strconv.Itoa(n)}}
		if err := p.api.Do(ctx, http.MethodGet, "projects", q, nil, &list); err != nil {
			return nil, err
		}
		for _, r := range list {
			out = append(out, tracker.Project{Key: r.Path, Name: r.Name, Description: r.Description, Logo: r.AvatarURL})
		}
		if len(list) < 100 {
			break
		}
	}
	return out, nil
}
