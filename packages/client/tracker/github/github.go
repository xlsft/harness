// Package github is GitHub Issues as a tracker. It signs in through the code host rather than a sign-in of its own (AuthVCS): on
// a device with the token of the gh login for the host (OpenOptions.VCSToken), on the server (packages/core/src/tracker/github.ts)
// through a connection from Settings > VCS. REST API: https://docs.github.com/en/rest/issues; search syntax:
// https://docs.github.com/en/search-github/searching-on-github/searching-issues-and-pull-requests. docs/design/tracker.md,
// "Writing a provider".
package github

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
const Name = "github"

func init() {
	tracker.Register(Name, tracker.Factory{Capabilities: Capabilities(), Open: Open})
}

// Capabilities describes the provider.
func Capabilities() tracker.Capabilities {
	return tracker.Capabilities{
		Provider: Name, DisplayName: "GitHub Issues",
		AuthMethods:   []tracker.AuthMethod{tracker.AuthVCS},
		QueryLanguage: "GitHub search", QueryExample: "repo:octo/app is:open label:bug",
		CustomFields: false, Comments: true, DefaultBaseURL: "https://github.com",
		OptionFields:       vcsOptionFields,
		DocsURL:            "https://docs.github.com/en/rest/issues",
		Logo:               "github",
		BaseURLPlaceholder: "https://github.com",
		AuthHints: map[tracker.AuthMethod]tracker.AuthHint{
			tracker.AuthVCS: {Help: "Signs in through GitHub: on this device the gh command line (gh auth login), for the company a connection from Settings → VCS."},
		},
	}
}

// vcsOptionFields are the code host settings of a sign-in through it; the same as VCS_OPTION_FIELDS on the server.
var vcsOptionFields = []tracker.OptionField{
	{Key: "vcs_connection_id", Label: "Code host connection", Help: "The id of a connection from Settings > VCS. On a device, gh or glab signs in instead."},
	{Key: "vcs_owner", Label: "GitHub account or organization", Help: "Which installation of the GitHub App to use; the first one when empty."},
}

// APIBase is the REST root of a GitHub address: api.github.com, or /api/v3 on Enterprise Server.
func APIBase(base string) string {
	u, err := url.Parse(base)
	if err == nil && strings.EqualFold(u.Hostname(), "github.com") {
		return "https://api.github.com"
	}
	return strings.TrimRight(base, "/") + "/api/v3"
}

// Provider implements tracker.Provider.
type Provider struct {
	api *tracker.JSONClient

	mu    sync.Mutex
	login string
}

var _ tracker.Provider = (*Provider)(nil)

// Open makes a provider for a connection.
func Open(o tracker.OpenOptions) (tracker.Provider, error) {
	if o.Auth != tracker.AuthVCS {
		return nil, errors.New("GitHub Issues signs in through the code host")
	}
	if o.VCSToken == nil {
		return nil, fmt.Errorf("%w: no code host sign-in is available here", tracker.ErrAuth)
	}
	base := tracker.NormalizeURL(o.BaseURL)
	if base == "" {
		base = "https://github.com"
	}
	host := "github.com"
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Host
	}
	var (
		mu    sync.Mutex
		token string
	)
	c := &tracker.JSONClient{
		Base: APIBase(base), HTTP: tracker.NewHTTPClient(), Name: "GitHub",
		Header: http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {"2022-11-28"}},
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

// ParseID splits owner/repo#123.
func ParseID(id string) (repo string, number int, err error) {
	repo, n, ok := strings.Cut(strings.TrimSpace(id), "#")
	number, convErr := strconv.Atoi(n)
	if !ok || convErr != nil || number <= 0 || strings.Count(repo, "/") != 1 {
		return "", 0, fmt.Errorf("%w: %q is not owner/repo#number", tracker.ErrNotFound, id)
	}
	return repo, number, nil
}

func repoPath(repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// Test implements tracker.Provider.
func (p *Provider) Test(ctx context.Context) (tracker.Identity, error) {
	var me struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := p.api.Do(ctx, http.MethodGet, "user", nil, nil, &me); err != nil {
		return tracker.Identity{}, err
	}
	p.mu.Lock()
	p.login = me.Login
	p.mu.Unlock()
	return tracker.Identity{Login: me.Login, DisplayName: me.Name}, nil
}

type apiIssue struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	HTMLURL       string `json:"html_url"`
	State         string `json:"state"`
	StateReason   string `json:"state_reason"`
	UpdatedAt     string `json:"updated_at"`
	RepositoryURL string `json:"repository_url"`
	PullRequest   any    `json:"pull_request"`
	Labels        []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

// repoOf reads owner/repo from an API repository URL.
func repoOf(u string) string {
	i := strings.LastIndex(u, "/repos/")
	if i < 0 {
		return ""
	}
	return u[i+len("/repos/"):]
}

func (i apiIssue) toIssue(repo string) tracker.Issue {
	if repo == "" {
		repo = repoOf(i.RepositoryURL)
	}
	out := tracker.Issue{ID: repo + "#" + strconv.Itoa(i.Number), Title: i.Title, URL: i.HTMLURL, Description: i.Body, Project: repo, UpdatedAt: i.UpdatedAt}
	add := func(name, v string) {
		if v != "" {
			out.Fields = append(out.Fields, tracker.FieldValue{Name: name, Value: v})
		}
	}
	state := i.State
	if state == "closed" && i.StateReason == "not_planned" {
		state = "closed as not planned"
	}
	add("State", state)
	var names []string
	for _, l := range i.Labels {
		names = append(names, l.Name)
	}
	add("Labels", strings.Join(names, ", "))
	names = nil
	for _, a := range i.Assignees {
		names = append(names, a.Login)
	}
	add("Assignees", strings.Join(names, ", "))
	if i.Milestone != nil {
		add("Milestone", i.Milestone.Title)
	}
	return out
}

// SearchQuery keeps pull requests out unless the query asks for them.
func SearchQuery(q string) string {
	low := " " + strings.ToLower(q) + " "
	if strings.Contains(low, " is:issue ") || strings.Contains(low, " is:pr ") || strings.Contains(low, " type:") || strings.Contains(low, " is:pull-request ") {
		return strings.TrimSpace(q)
	}
	return strings.TrimSpace(q + " is:issue")
}

// Search implements tracker.Provider, with GitHub's search syntax.
func (p *Provider) Search(ctx context.Context, query string, page tracker.Page) ([]tracker.Issue, bool, error) {
	limit := max(page.Limit, 1)
	const per = 100
	n := page.Offset/per + 1
	skip := page.Offset % per
	var out []tracker.Issue
	for ; n <= 10; n++ {
		var res struct {
			Total int        `json:"total_count"`
			Items []apiIssue `json:"items"`
		}
		q := url.Values{"q": {SearchQuery(query)}, "per_page": {strconv.Itoa(per)}, "page": {strconv.Itoa(n)}}
		if err := p.api.Do(ctx, http.MethodGet, "search/issues", q, nil, &res); err != nil {
			return nil, false, err
		}
		for _, i := range res.Items {
			if skip > 0 {
				skip--
				continue
			}
			if i.PullRequest != nil {
				continue
			}
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, i.toIssue(""))
		}
		if len(res.Items) < per || n*per >= res.Total {
			return out, false, nil
		}
	}
	return out, false, nil
}

// Get implements tracker.Provider.
func (p *Provider) Get(ctx context.Context, id string) (*tracker.Issue, error) {
	repo, n, err := ParseID(id)
	if err != nil {
		return nil, err
	}
	path := repoPath(repo) + "/issues/" + strconv.Itoa(n)
	var raw apiIssue
	if err := p.api.Do(ctx, http.MethodGet, path, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := raw.toIssue(repo)
	var comments []struct {
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		User      *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := p.api.Do(ctx, http.MethodGet, path+"/comments", url.Values{"per_page": {"100"}}, nil, &comments); err != nil {
		return nil, err
	}
	for _, c := range comments {
		author := ""
		if c.User != nil {
			author = c.User.Login
		}
		out.Comments = append(out.Comments, tracker.IssueComment{Author: author, Text: c.Body, CreatedAt: c.CreatedAt})
	}
	if k := len(out.Comments); k > 10 {
		out.Comments = out.Comments[k-10:]
	}
	return &out, nil
}

// Create implements tracker.Provider. The project is owner/repo.
func (p *Provider) Create(ctx context.Context, d tracker.IssueDraft) (*tracker.Issue, error) {
	if strings.Count(d.Project, "/") != 1 {
		return nil, fmt.Errorf("%q is not owner/repo", d.Project)
	}
	var raw apiIssue
	if err := p.api.Do(ctx, http.MethodPost, repoPath(d.Project)+"/issues", nil, map[string]any{"title": d.Title, "body": d.Description}, &raw); err != nil {
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

func (p *Provider) me(ctx context.Context) (string, error) {
	p.mu.Lock()
	login := p.login
	p.mu.Unlock()
	if login != "" {
		return login, nil
	}
	id, err := p.Test(ctx)
	return id.Login, err
}

// Update implements tracker.Provider: State (open, closed, closed as not planned), Labels, Add labels, Remove labels, Assignees
// ("me" is the signed-in account), Milestone, Title and Description.
func (p *Provider) Update(ctx context.Context, id string, u tracker.IssueUpdate) error {
	repo, n, err := ParseID(id)
	if err != nil {
		return err
	}
	path := repoPath(repo) + "/issues/" + strconv.Itoa(n)
	body := map[string]any{}
	if u.Title != nil {
		body["title"] = *u.Title
	}
	if u.Description != nil {
		body["body"] = *u.Description
	}
	var add, rm []string
	for _, f := range u.Fields {
		switch strings.ToLower(strings.TrimSpace(f.Name)) {
		case "state", "status", "stage":
			switch v := strings.ToLower(strings.TrimSpace(f.Value)); v {
			case "open", "reopened":
				body["state"] = "open"
			case "closed", "completed", "done", "closed as completed":
				body["state"], body["state_reason"] = "closed", "completed"
			case "not planned", "closed as not planned", "not_planned":
				body["state"], body["state_reason"] = "closed", "not_planned"
			default:
				return fmt.Errorf("%q is not a state of a GitHub issue: open, closed or closed as not planned", f.Value)
			}
		case "labels":
			body["labels"] = splitList(f.Value)
		case "add labels":
			add = append(add, splitList(f.Value)...)
		case "remove labels":
			rm = append(rm, splitList(f.Value)...)
		case "assignees", "assignee":
			list := []string{}
			for _, a := range splitList(f.Value) {
				if strings.EqualFold(a, "me") {
					if a, err = p.me(ctx); err != nil {
						return err
					}
				}
				if !strings.EqualFold(a, "none") {
					list = append(list, a)
				}
			}
			body["assignees"] = list
		case "milestone":
			m, err := p.milestone(ctx, repo, f.Value)
			if err != nil {
				return err
			}
			body["milestone"] = m
		case "title":
			body["title"] = f.Value
		case "description", "body":
			body["body"] = f.Value
		default:
			return fmt.Errorf("unknown field %q: GitHub Issues sets State, Labels, Add labels, Remove labels, Assignees, Milestone, Title and Description", f.Name)
		}
	}
	if len(body) > 0 {
		if err := p.api.Do(ctx, http.MethodPatch, path, nil, body, nil); err != nil {
			return err
		}
	}
	if len(add) > 0 {
		if err := p.api.Do(ctx, http.MethodPost, path+"/labels", nil, map[string]any{"labels": add}, nil); err != nil {
			return err
		}
	}
	for _, l := range rm {
		if err := p.api.Do(ctx, http.MethodDelete, path+"/labels/"+url.PathEscape(l), nil, nil, nil); err != nil && !errors.Is(err, tracker.ErrNotFound) {
			return err
		}
	}
	return nil
}

type milestone struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

func (p *Provider) milestones(ctx context.Context, repo string) ([]milestone, error) {
	var list []milestone
	err := p.api.Do(ctx, http.MethodGet, repoPath(repo)+"/milestones", url.Values{"state": {"all"}, "per_page": {"100"}}, nil, &list)
	return list, err
}

// milestone is a milestone's number by title; nil for none.
func (p *Provider) milestone(ctx context.Context, repo, title string) (any, error) {
	if t := strings.TrimSpace(title); t == "" || strings.EqualFold(t, "none") {
		return nil, nil
	}
	list, err := p.milestones(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		if strings.EqualFold(m.Title, strings.TrimSpace(title)) {
			return m.Number, nil
		}
	}
	return nil, fmt.Errorf("no milestone %q in %s", title, repo)
}

// Comment implements tracker.Provider.
func (p *Provider) Comment(ctx context.Context, id, body string) error {
	repo, n, err := ParseID(id)
	if err != nil {
		return err
	}
	return p.api.Do(ctx, http.MethodPost, repoPath(repo)+"/issues/"+strconv.Itoa(n)+"/comments", nil, map[string]any{"body": body}, nil)
}

// Fields implements tracker.Provider: the project is owner/repo.
func (p *Provider) Fields(ctx context.Context, project string) ([]tracker.Field, error) {
	labels := tracker.Field{Name: "Labels", Kind: "labels", Multi: true}
	assignees := tracker.Field{Name: "Assignees", Kind: "user", Multi: true}
	ms := tracker.Field{Name: "Milestone", Kind: "bundle"}
	if project != "" {
		var ls []struct {
			Name string `json:"name"`
		}
		if err := p.api.Do(ctx, http.MethodGet, repoPath(project)+"/labels", url.Values{"per_page": {"100"}}, nil, &ls); err != nil {
			return nil, err
		}
		for _, l := range ls {
			labels.Values = append(labels.Values, l.Name)
		}
		var as []struct {
			Login string `json:"login"`
		}
		if err := p.api.Do(ctx, http.MethodGet, repoPath(project)+"/assignees", url.Values{"per_page": {"100"}}, nil, &as); err == nil {
			for _, a := range as {
				assignees.Values = append(assignees.Values, a.Login)
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
	return []tracker.Field{{Name: "State", Kind: "state", Values: []string{"open", "closed", "closed as not planned"}}, labels, add, rm, assignees, ms}, nil
}

// Projects implements tracker.Provider: the repositories the account works in, most recently updated first.
func (p *Provider) Projects(ctx context.Context) ([]tracker.Project, error) {
	var out []tracker.Project
	for n := 1; n <= 3; n++ {
		var list []struct {
			FullName    string `json:"full_name"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Archived    bool   `json:"archived"`
			HasIssues   bool   `json:"has_issues"`
			Owner       struct {
				AvatarURL string `json:"avatar_url"`
			} `json:"owner"`
		}
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(n)}, "sort": {"updated"}}
		if err := p.api.Do(ctx, http.MethodGet, "user/repos", q, nil, &list); err != nil {
			return nil, err
		}
		for _, r := range list {
			if !r.Archived && r.HasIssues {
				out = append(out, tracker.Project{Key: r.FullName, Name: r.Name, Description: r.Description, Logo: r.Owner.AvatarURL})
			}
		}
		if len(list) < 100 {
			break
		}
	}
	return out, nil
}
