// Package jira is the Jira tracker provider, Cloud and Data Center, and the Confluence knowledge provider, which can borrow a
// Jira connection's sign-in on the same site. docs/design/tracker.md, "Writing a provider". The server mirrors it in
// packages/core/src/tracker/jira.ts and packages/core/src/knowledge/confluence.ts for company connections.
//
// Sign-in, checked against Atlassian's documentation:
//   - Cloud: an account email and an API token as basic auth
//     (https://developer.atlassian.com/cloud/jira/platform/basic-auth-for-rest-apis/).
//   - Data Center: a personal access token as a bearer token
//     (https://confluence.atlassian.com/enterprise/using-personal-access-tokens-1026032365.html).
//   - OAuth 2.0 (3LO) on Cloud through auth.atlassian.com and the api.atlassian.com gateway
//     (https://developer.atlassian.com/cloud/jira/platform/oauth-2-3lo-apps/); on Data Center the built-in OAuth 2.0 provider
//     (https://confluence.atlassian.com/adminjiraserver/jira-oauth-2-0-provider-api-1115659070.html), both with PKCE.
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/harness/packages/client/tracker"
)

// Name is this provider's name in a connection; KnowledgeName the Confluence knowledge provider's.
const (
	Name          = "jira"
	KnowledgeName = "confluence"
)

// Atlassian's OAuth 2.0 (3LO) hosts for Cloud. Tests point them at a fake.
var (
	AuthURL    = "https://auth.atlassian.com"
	GatewayURL = "https://api.atlassian.com"
)

// IsCloud says whether an address is Jira Cloud. Tests replace it, as their fake has a loopback address.
var IsCloud = func(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return strings.HasSuffix(h, ".atlassian.net") || strings.HasSuffix(h, ".jira.com")
}

// OAuthScopes are what a Cloud connection asks for: Jira issues, users for assignees, Confluence pages, and a refresh token.
const OAuthScopes = "read:jira-work write:jira-work read:jira-user read:confluence-content.all read:confluence-space.summary search:confluence offline_access"

// fieldsTTL is how long the field list is reused: a run of outputs costs one request.
const fieldsTTL = 5 * time.Minute

func init() {
	tracker.Register(Name, tracker.Factory{
		Capabilities: Capabilities(),
		Open:         Open,
		OAuth: func(baseURL string) (tracker.OAuthExchange, error) {
			u, err := url.Parse(tracker.NormalizeURL(baseURL))
			if err != nil || u.Host == "" {
				return nil, errors.New("the address must be an http(s) URL")
			}
			return oauthExchange{site: strings.TrimRight(u.String(), "/"), cloud: IsCloud(u)}, nil
		},
	})
	tracker.RegisterKnowledge(KnowledgeName, tracker.KnowledgeFactory{
		Capabilities: KnowledgeCapabilities(),
		Open:         OpenKnowledge,
		OAuth: func(baseURL string) (tracker.OAuthExchange, error) {
			u, err := url.Parse(strings.TrimSuffix(strings.TrimRight(tracker.NormalizeURL(baseURL), "/"), "/wiki"))
			if err != nil || u.Host == "" {
				return nil, errors.New("the address must be an http(s) URL")
			}
			return oauthExchange{site: strings.TrimRight(u.String(), "/"), cloud: IsCloud(u)}, nil
		},
	})
}

var authHints = map[tracker.AuthMethod]tracker.AuthHint{
	tracker.AuthEmailToken: {Help: "Jira Cloud: your Atlassian account email and an API token from id.atlassian.com → Security → API tokens.", SecretLabel: "API token", LoginLabel: "Account email"},
	tracker.AuthToken:      {Help: "Jira Data Center: a personal access token from your profile → Personal Access Tokens.", SecretLabel: "Personal access token"},
	tracker.AuthOAuth:      {Help: "An OAuth 2.0 app: on Cloud from developer.atlassian.com (3LO), on Data Center an incoming link with OAuth 2.0."},
}

// Capabilities describes the provider.
func Capabilities() tracker.Capabilities {
	return tracker.Capabilities{
		Provider: Name, DisplayName: "Jira",
		AuthMethods:   []tracker.AuthMethod{tracker.AuthEmailToken, tracker.AuthToken, tracker.AuthOAuth},
		QueryLanguage: "JQL", QueryExample: `project = DEMO AND status = "To Do"`,
		CustomFields: true, Comments: true,
		DocsURL:            "https://support.atlassian.com/atlassian-account/docs/manage-api-tokens-for-your-atlassian-account/",
		Logo:               "jira",
		BaseURLPlaceholder: "https://your-site.atlassian.net",
		AuthHints:          authHints,
	}
}

// KnowledgeCapabilities describes Confluence.
func KnowledgeCapabilities() tracker.KnowledgeCapabilities {
	return tracker.KnowledgeCapabilities{
		Provider: KnowledgeName, DisplayName: "Confluence",
		Description:   "Pages of Confluence spaces, Cloud or Data Center.",
		AuthMethods:   []tracker.AuthMethod{tracker.AuthEmailToken, tracker.AuthToken, tracker.AuthOAuth},
		DocsURL:       "https://developer.atlassian.com/cloud/confluence/rest/v1/intro/",
		Logo:          "confluence",
		BorrowFrom:    []string{Name},
		QueryLanguage: "CQL", QueryExample: `space = DOCS`,
		BaseURLPlaceholder: "https://your-site.atlassian.net/wiki",
		AuthHints:          authHints,
		Tree:               true,
	}
}

type oauthExchange struct {
	site  string
	cloud bool
}

func (o oauthExchange) AuthorizeURL(_ context.Context, clientID, redirectURI, state, challenge string) (string, error) {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirectURI}, "state": {state}, "response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if o.cloud {
		q.Set("audience", "api.atlassian.com")
		q.Set("scope", OAuthScopes)
		q.Set("prompt", "consent")
		return AuthURL + "/authorize?" + q.Encode(), nil
	}
	q.Set("scope", "WRITE")
	return o.site + "/rest/oauth2/latest/authorize?" + q.Encode(), nil
}

func (o oauthExchange) Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI, verifier string) (tracker.Credentials, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	return tracker.OAuthTokens(ctx, nil, o.tokenURL(), form, o.cloud)
}

func (o oauthExchange) tokenURL() string {
	if o.cloud {
		return AuthURL + "/oauth/token"
	}
	return o.site + "/rest/oauth2/latest/token"
}

// Provider implements tracker.Provider and tracker.KnowledgeProvider.
type Provider struct {
	site      string
	cloud     bool
	auth      tracker.AuthMethod
	knowledge string
	authorize func(ctx context.Context, h http.Header) error
	hc        *http.Client

	mu      sync.Mutex
	cloudID string
	fields  []apiField
	fieldAt time.Time
	me      string
}

var (
	_ tracker.Provider          = (*Provider)(nil)
	_ tracker.KnowledgeProvider = (*Provider)(nil)
)

// Open makes a tracker provider for a connection.
func Open(o tracker.OpenOptions) (tracker.Provider, error) {
	return open(o, "")
}

// OpenKnowledge makes Confluence for a knowledge connection. With a Jira connection's sign-in on Cloud, Confluence is that site
// under /wiki; an address of its own (Data Center, where it has one) is used as it is.
func OpenKnowledge(o tracker.KnowledgeOpenOptions) (tracker.KnowledgeProvider, error) {
	own := strings.TrimRight(tracker.NormalizeURL(o.BaseURL), "/")
	site, wiki := own, ""
	if o.BorrowedFrom != "" && o.TrackerBaseURL != "" {
		site = strings.TrimRight(tracker.NormalizeURL(o.TrackerBaseURL), "/")
	}
	u, err := url.Parse(site)
	if err != nil || u.Host == "" {
		return nil, errors.New("the address must be an http(s) URL")
	}
	if IsCloud(u) {
		// Cloud: the site, with or without /wiki, is enough.
		site = strings.TrimSuffix(site, "/wiki")
	} else if own != "" && own != site || o.BorrowedFrom == "" {
		wiki = own
	}
	p, err := open(tracker.OpenOptions{BaseURL: site, Auth: o.Auth, Login: o.Login, OAuthClientID: o.OAuthClientID, Credentials: o.Credentials, Options: o.Options, OnTokens: o.OnTokens}, wiki)
	if err != nil {
		return nil, err
	}
	return p.(*Provider), nil
}

func open(o tracker.OpenOptions, wiki string) (tracker.Provider, error) {
	u, err := url.Parse(tracker.NormalizeURL(o.BaseURL))
	if err != nil || u.Host == "" {
		return nil, errors.New("the address must be an http(s) URL")
	}
	p := &Provider{site: strings.TrimRight(u.String(), "/"), cloud: IsCloud(u), auth: o.Auth, knowledge: strings.TrimRight(wiki, "/"), hc: tracker.NewHTTPClient()}
	switch o.Auth {
	case tracker.AuthEmailToken:
		if o.Credentials.Secret == "" {
			return nil, fmt.Errorf("%w: no API token is stored for this connection", tracker.ErrAuth)
		}
		basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(o.Login+":"+o.Credentials.Secret))
		p.authorize = func(_ context.Context, h http.Header) error { h.Set("Authorization", basic); return nil }
	case tracker.AuthToken:
		if o.Credentials.Secret == "" {
			return nil, fmt.Errorf("%w: no personal access token is stored for this connection", tracker.ErrAuth)
		}
		bearer := "Bearer " + o.Credentials.Secret
		p.authorize = func(_ context.Context, h http.Header) error { h.Set("Authorization", bearer); return nil }
	case tracker.AuthOAuth:
		ex := oauthExchange{site: p.site, cloud: p.cloud}
		ts := &tracker.TokenSource{Creds: o.Credentials, OnTokens: o.OnTokens, Refresh: func(ctx context.Context, refresh string) (tracker.Credentials, error) {
			form := url.Values{"grant_type": {"refresh_token"}, "client_id": {o.OAuthClientID}, "refresh_token": {refresh}}
			if o.Credentials.ClientSecret != "" {
				form.Set("client_secret", o.Credentials.ClientSecret)
			}
			return tracker.OAuthTokens(ctx, p.hc, ex.tokenURL(), form, p.cloud)
		}}
		p.authorize = func(ctx context.Context, h http.Header) error {
			t, err := ts.Token(ctx)
			if err != nil {
				return err
			}
			h.Set("Authorization", "Bearer "+t)
			return nil
		}
	default:
		return nil, fmt.Errorf("Jira does not sign in with %q", o.Auth)
	}
	return p, nil
}

// gateway: an OAuth connection to Cloud goes through api.atlassian.com, under the site's cloud id.
func (p *Provider) gateway() bool { return p.cloud && p.auth == tracker.AuthOAuth }

func (p *Provider) resolveCloudID(ctx context.Context) (string, error) {
	p.mu.Lock()
	id := p.cloudID
	p.mu.Unlock()
	if id != "" {
		return id, nil
	}
	c := &tracker.JSONClient{Base: GatewayURL, Authorize: p.authorize, HTTP: p.hc, Name: "Atlassian"}
	var sites []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := c.Do(ctx, http.MethodGet, "oauth/token/accessible-resources", nil, nil, &sites); err != nil {
		return "", err
	}
	for _, s := range sites {
		if strings.EqualFold(strings.TrimRight(s.URL, "/"), p.site) {
			id = s.ID
		}
	}
	if id == "" {
		return "", fmt.Errorf("%w: the authorization does not cover %s", tracker.ErrForbidden, p.site)
	}
	p.mu.Lock()
	p.cloudID = id
	p.mu.Unlock()
	return id, nil
}

func (p *Provider) jira(ctx context.Context) (*tracker.JSONClient, error) {
	base := p.site + "/rest/api/2"
	if p.gateway() {
		id, err := p.resolveCloudID(ctx)
		if err != nil {
			return nil, err
		}
		base = GatewayURL + "/ex/jira/" + id + "/rest/api/2"
	}
	return &tracker.JSONClient{Base: base, Authorize: p.authorize, HTTP: p.hc, Name: "Jira"}, nil
}

// wiki is Confluence's REST root: under the site on Cloud, through the gateway with OAuth, at its own address on Data Center.
func (p *Provider) wiki(ctx context.Context) (*tracker.JSONClient, string, error) {
	var base, web string
	switch {
	case p.knowledge != "":
		base, web = p.knowledge+"/rest/api", p.knowledge
	case p.gateway():
		id, err := p.resolveCloudID(ctx)
		if err != nil {
			return nil, "", err
		}
		base, web = GatewayURL+"/ex/confluence/"+id+"/wiki/rest/api", p.site+"/wiki"
	case p.cloud:
		base, web = p.site+"/wiki/rest/api", p.site+"/wiki"
	default:
		return nil, "", errors.New("set the Confluence address of this connection: on Data Center it has its own")
	}
	return &tracker.JSONClient{Base: base, Authorize: p.authorize, HTTP: p.hc, Name: "Confluence"}, web, nil
}

// Capabilities implements tracker.Provider.
func (*Provider) Capabilities() tracker.Capabilities { return Capabilities() }

type apiUser struct {
	AccountID    string `json:"accountId"`
	Name         string `json:"name"`
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
}

// Test implements tracker.Provider.
func (p *Provider) Test(ctx context.Context) (tracker.Identity, error) {
	c, err := p.jira(ctx)
	if err != nil {
		return tracker.Identity{}, err
	}
	var me apiUser
	if err := c.Do(ctx, http.MethodGet, "myself", nil, nil, &me); err != nil {
		return tracker.Identity{}, err
	}
	login := me.EmailAddress
	if login == "" {
		login = me.Name
	}
	if login == "" {
		login = me.AccountID
	}
	return tracker.Identity{Login: login, DisplayName: me.DisplayName}, nil
}

type apiIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Updated     string          `json:"updated"`
		Project     struct {
			Key string `json:"key"`
		} `json:"project"`
		Status *struct {
			Name string `json:"name"`
		} `json:"status"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Assignee *apiUser `json:"assignee"`
		Labels   []string `json:"labels"`
		Comment  *struct {
			Comments []struct {
				Author  *apiUser        `json:"author"`
				Body    json.RawMessage `json:"body"`
				Created string          `json:"created"`
			} `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}

// jiraTime makes Jira's 2024-01-02T03:04:05.000+0000 into RFC 3339.
func jiraTime(s string) string {
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}

// text reads a v2 string body, or the text of a v3 document when a proxy sends one.
func text(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var doc struct {
		Text    string            `json:"text"`
		Content []json.RawMessage `json:"content"`
		Type    string            `json:"type"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	out := doc.Text
	for _, c := range doc.Content {
		out += text(c)
	}
	if doc.Type == "paragraph" || doc.Type == "heading" {
		out += "\n"
	}
	return out
}

func (p *Provider) issue(i apiIssue) tracker.Issue {
	f := i.Fields
	out := tracker.Issue{ID: i.Key, Title: f.Summary, URL: p.site + "/browse/" + i.Key, Description: strings.TrimSpace(text(f.Description)), Project: f.Project.Key, UpdatedAt: jiraTime(f.Updated)}
	add := func(name, v string) {
		if v != "" {
			out.Fields = append(out.Fields, tracker.FieldValue{Name: name, Value: v})
		}
	}
	if f.Status != nil {
		add("Status", f.Status.Name)
	}
	if f.IssueType != nil {
		add("Type", f.IssueType.Name)
	}
	if f.Priority != nil {
		add("Priority", f.Priority.Name)
	}
	if f.Assignee != nil {
		add("Assignee", f.Assignee.DisplayName)
	}
	add("Labels", strings.Join(f.Labels, ", "))
	if f.Comment != nil {
		for _, c := range f.Comment.Comments {
			author := ""
			if c.Author != nil {
				author = c.Author.DisplayName
			}
			out.Comments = append(out.Comments, tracker.IssueComment{Author: author, Text: strings.TrimSpace(text(c.Body)), CreatedAt: jiraTime(c.Created)})
		}
		if n := len(out.Comments); n > 10 {
			out.Comments = out.Comments[n-10:]
		}
	}
	return out
}

const listFields = "summary,updated,project"

// Search implements tracker.Provider. Cloud pages with nextPageToken (its search/jql has no offset), Data Center with startAt.
func (p *Provider) Search(ctx context.Context, query string, page tracker.Page) ([]tracker.Issue, bool, error) {
	c, err := p.jira(ctx)
	if err != nil {
		return nil, false, err
	}
	limit := max(page.Limit, 1)
	var out []tracker.Issue
	if !p.cloud {
		var res struct {
			Issues []apiIssue `json:"issues"`
			Total  int        `json:"total"`
		}
		q := url.Values{"jql": {query}, "fields": {listFields}, "startAt": {strconv.Itoa(page.Offset)}, "maxResults": {strconv.Itoa(limit)}}
		if err := c.Do(ctx, http.MethodGet, "search", q, nil, &res); err != nil {
			return nil, false, err
		}
		for _, i := range res.Issues {
			out = append(out, p.issue(i))
		}
		return out, page.Offset+len(res.Issues) < res.Total, nil
	}
	skip, token := page.Offset, ""
	for {
		var res struct {
			Issues        []apiIssue `json:"issues"`
			NextPageToken string     `json:"nextPageToken"`
			IsLast        bool       `json:"isLast"`
		}
		q := url.Values{"jql": {query}, "fields": {listFields}, "maxResults": {strconv.Itoa(min(100, skip+limit-len(out)+1))}}
		if token != "" {
			q.Set("nextPageToken", token)
		}
		if err := c.Do(ctx, http.MethodGet, "search/jql", q, nil, &res); err != nil {
			return nil, false, err
		}
		for _, i := range res.Issues {
			if skip > 0 {
				skip--
				continue
			}
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, p.issue(i))
		}
		if res.NextPageToken == "" || res.IsLast || len(res.Issues) == 0 {
			return out, false, nil
		}
		token = res.NextPageToken
	}
}

// Get implements tracker.Provider.
func (p *Provider) Get(ctx context.Context, id string) (*tracker.Issue, error) {
	c, err := p.jira(ctx)
	if err != nil {
		return nil, err
	}
	var raw apiIssue
	q := url.Values{"fields": {"summary,description,updated,project,status,priority,issuetype,assignee,labels,comment"}}
	if err := c.Do(ctx, http.MethodGet, "issue/"+url.PathEscape(id), q, nil, &raw); err != nil {
		return nil, err
	}
	out := p.issue(raw)
	return &out, nil
}

// Create implements tracker.Provider. The type is Task unless a field named Type says otherwise.
func (p *Provider) Create(ctx context.Context, d tracker.IssueDraft) (*tracker.Issue, error) {
	c, err := p.jira(ctx)
	if err != nil {
		return nil, err
	}
	kind, rest := "Task", []tracker.FieldValue{}
	for _, f := range d.Fields {
		if strings.EqualFold(f.Name, "Type") || strings.EqualFold(f.Name, "Issue Type") {
			kind = f.Value
		} else {
			rest = append(rest, f)
		}
	}
	fields := map[string]any{"project": map[string]any{"key": d.Project}, "summary": d.Title, "issuetype": map[string]any{"name": kind}}
	if d.Description != "" {
		fields["description"] = d.Description
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := c.Do(ctx, http.MethodPost, "issue", nil, map[string]any{"fields": fields}, &created); err != nil {
		return nil, err
	}
	if len(rest) > 0 {
		if err := p.Update(ctx, created.Key, tracker.IssueUpdate{Fields: rest}); err != nil {
			return nil, err
		}
	}
	return &tracker.Issue{ID: created.Key, Title: d.Title, URL: p.site + "/browse/" + created.Key, Project: d.Project}, nil
}

type apiField struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema *struct {
		Type   string `json:"type"`
		Items  string `json:"items"`
		System string `json:"system"`
	} `json:"schema"`
}

func (f apiField) kind() string {
	if f.Schema == nil {
		return "string"
	}
	switch f.Schema.Type {
	case "status":
		return "state"
	case "priority", "option", "issuetype", "resolution":
		return "bundle"
	case "user":
		return "user"
	case "number":
		return "float"
	case "date", "datetime":
		return "date"
	case "array":
		if f.Schema.Items == "string" {
			return "labels"
		}
		return "bundle"
	}
	if f.Schema.System == "description" {
		return "text"
	}
	return "string"
}

func (p *Provider) allFields(ctx context.Context, fresh bool) ([]apiField, error) {
	p.mu.Lock()
	cached, at := p.fields, p.fieldAt
	p.mu.Unlock()
	if !fresh && cached != nil && time.Since(at) < fieldsTTL {
		return cached, nil
	}
	c, err := p.jira(ctx)
	if err != nil {
		return nil, err
	}
	var list []apiField
	if err := c.Do(ctx, http.MethodGet, "field", nil, nil, &list); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.fields, p.fieldAt = list, time.Now()
	p.mu.Unlock()
	return list, nil
}

func isStatus(name string) bool {
	return strings.EqualFold(name, "status") || strings.EqualFold(name, "state") || strings.EqualFold(name, "stage")
}

// Update implements tracker.Provider. Status goes through a transition, as Jira allows nothing else; "Add labels" and
// "Remove labels" change labels without replacing them.
func (p *Provider) Update(ctx context.Context, id string, u tracker.IssueUpdate) error {
	c, err := p.jira(ctx)
	if err != nil {
		return err
	}
	fields, update := map[string]any{}, map[string]any{}
	if u.Title != nil {
		fields["summary"] = *u.Title
	}
	if u.Description != nil {
		fields["description"] = *u.Description
	}
	status := ""
	for _, f := range u.Fields {
		switch {
		case isStatus(f.Name):
			status = f.Value
		case strings.EqualFold(f.Name, "Add labels") || strings.EqualFold(f.Name, "Remove labels"):
			verb := "add"
			if strings.HasPrefix(strings.ToLower(f.Name), "remove") {
				verb = "remove"
			}
			ops, _ := update["labels"].([]any)
			for _, l := range splitList(f.Value) {
				ops = append(ops, map[string]any{verb: l})
			}
			update["labels"] = ops
		default:
			fid, v, err := p.encode(ctx, f)
			if err != nil {
				return err
			}
			fields[fid] = v
		}
	}
	if len(fields)+len(update) > 0 {
		body := map[string]any{}
		if len(fields) > 0 {
			body["fields"] = fields
		}
		if len(update) > 0 {
			body["update"] = update
		}
		if err := c.Do(ctx, http.MethodPut, "issue/"+url.PathEscape(id), nil, body, nil); err != nil {
			return err
		}
	}
	if status != "" {
		return p.transition(ctx, c, id, status)
	}
	return nil
}

func (p *Provider) transition(ctx context.Context, c *tracker.JSONClient, id, status string) error {
	var res struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	path := "issue/" + url.PathEscape(id) + "/transitions"
	if err := c.Do(ctx, http.MethodGet, path, nil, nil, &res); err != nil {
		return err
	}
	var names []string
	for _, t := range res.Transitions {
		if strings.EqualFold(t.To.Name, status) || strings.EqualFold(t.Name, status) {
			return c.Do(ctx, http.MethodPost, path, nil, map[string]any{"transition": map[string]any{"id": t.ID}}, nil)
		}
		names = append(names, t.To.Name)
	}
	return fmt.Errorf("%s can't move to %q from where it is; it can move to: %s", id, status, strings.Join(names, ", "))
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

// encode finds a field by name or id and shapes the value for it.
func (p *Provider) encode(ctx context.Context, v tracker.FieldValue) (string, any, error) {
	find := func(list []apiField) *apiField {
		for i := range list {
			if strings.EqualFold(list[i].Name, v.Name) || list[i].ID == v.Name {
				return &list[i]
			}
		}
		return nil
	}
	list, err := p.allFields(ctx, false)
	if err != nil {
		return "", nil, err
	}
	f := find(list)
	if f == nil {
		if list, err = p.allFields(ctx, true); err != nil {
			return "", nil, err
		}
		if f = find(list); f == nil {
			return "", nil, fmt.Errorf("unknown field %q", v.Name)
		}
	}
	typ, items := "string", ""
	if f.Schema != nil {
		typ, items = f.Schema.Type, f.Schema.Items
	}
	switch typ {
	case "number":
		n, err := strconv.ParseFloat(v.Value, 64)
		if err != nil {
			return "", nil, fmt.Errorf("field %q: %q is not a number", v.Name, v.Value)
		}
		return f.ID, n, nil
	case "priority", "issuetype", "resolution", "version", "component":
		return f.ID, map[string]any{"name": v.Value}, nil
	case "option":
		return f.ID, map[string]any{"value": v.Value}, nil
	case "user":
		u, err := p.user(ctx, v.Value)
		if err != nil {
			return "", nil, fmt.Errorf("field %q: %w", v.Name, err)
		}
		return f.ID, u, nil
	case "array":
		vals := splitList(v.Value)
		switch items {
		case "string":
			return f.ID, vals, nil
		case "option":
			out := make([]any, 0, len(vals))
			for _, x := range vals {
				out = append(out, map[string]any{"value": x})
			}
			return f.ID, out, nil
		case "version", "component":
			out := make([]any, 0, len(vals))
			for _, x := range vals {
				out = append(out, map[string]any{"name": x})
			}
			return f.ID, out, nil
		}
		return "", nil, fmt.Errorf("field %q: lists of %s can't be set from here", v.Name, items)
	case "string", "date", "datetime", "":
		return f.ID, v.Value, nil
	}
	return "", nil, fmt.Errorf("field %q: %s fields can't be set from here", v.Name, typ)
}

// user is how Jira names a user in a field: an account id on Cloud, a user name on Data Center. "me" is the connection's account.
func (p *Provider) user(ctx context.Context, who string) (map[string]any, error) {
	if who == "" || strings.EqualFold(who, "none") || strings.EqualFold(who, "unassigned") {
		return nil, nil
	}
	c, err := p.jira(ctx)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(who, "me") {
		var me apiUser
		if err := c.Do(ctx, http.MethodGet, "myself", nil, nil, &me); err != nil {
			return nil, err
		}
		if p.cloud {
			return map[string]any{"accountId": me.AccountID}, nil
		}
		return map[string]any{"name": me.Name}, nil
	}
	if !p.cloud {
		return map[string]any{"name": who}, nil
	}
	var found []apiUser
	if err := c.Do(ctx, http.MethodGet, "user/search", url.Values{"query": {who}}, nil, &found); err != nil {
		return nil, err
	}
	for _, u := range found {
		if strings.EqualFold(u.EmailAddress, who) || strings.EqualFold(u.DisplayName, who) || u.AccountID == who {
			return map[string]any{"accountId": u.AccountID}, nil
		}
	}
	if len(found) == 1 {
		return map[string]any{"accountId": found[0].AccountID}, nil
	}
	return nil, fmt.Errorf("no single user matches %q", who)
}

// Comment implements tracker.Provider.
func (p *Provider) Comment(ctx context.Context, id, body string) error {
	c, err := p.jira(ctx)
	if err != nil {
		return err
	}
	return c.Do(ctx, http.MethodPost, "issue/"+url.PathEscape(id)+"/comment", nil, map[string]any{"body": body}, nil)
}

// Fields implements tracker.Provider: Status with the project's statuses, Priority, Labels, Assignee, then custom fields.
func (p *Provider) Fields(ctx context.Context, project string) ([]tracker.Field, error) {
	c, err := p.jira(ctx)
	if err != nil {
		return nil, err
	}
	status := tracker.Field{Name: "Status", Kind: "state"}
	if project != "" {
		var types []struct {
			Statuses []struct {
				Name string `json:"name"`
			} `json:"statuses"`
		}
		if err := c.Do(ctx, http.MethodGet, "project/"+url.PathEscape(project)+"/statuses", nil, nil, &types); err != nil {
			return nil, err
		}
		for _, t := range types {
			for _, s := range t.Statuses {
				if !slices.Contains(status.Values, s.Name) {
					status.Values = append(status.Values, s.Name)
				}
			}
		}
	}
	out := []tracker.Field{status}
	var prios []struct {
		Name string `json:"name"`
	}
	if err := c.Do(ctx, http.MethodGet, "priority", nil, nil, &prios); err == nil {
		f := tracker.Field{Name: "Priority", Kind: "bundle"}
		for _, x := range prios {
			f.Values = append(f.Values, x.Name)
		}
		out = append(out, f)
	}
	out = append(out, tracker.Field{Name: "Assignee", Kind: "user"}, tracker.Field{Name: "Labels", Kind: "labels", Multi: true}, tracker.Field{Name: "Add labels", Kind: "labels", Multi: true}, tracker.Field{Name: "Remove labels", Kind: "labels", Multi: true})
	list, err := p.allFields(ctx, false)
	if err != nil {
		return nil, err
	}
	var custom []tracker.Field
	for _, f := range list {
		if f.Custom {
			custom = append(custom, tracker.Field{Name: f.Name, Kind: f.kind(), Multi: f.Schema != nil && f.Schema.Type == "array"})
		}
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].Name < custom[j].Name })
	return append(out, custom...), nil
}

// Projects implements tracker.Provider.
func (p *Provider) Projects(ctx context.Context) ([]tracker.Project, error) {
	c, err := p.jira(ctx)
	if err != nil {
		return nil, err
	}
	type apiProject struct {
		Key         string            `json:"key"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Avatars     map[string]string `json:"avatarUrls"`
		Archived    bool              `json:"archived"`
	}
	var list []apiProject
	if p.cloud {
		for start := 0; start < 2000; {
			var page struct {
				Values []apiProject `json:"values"`
				IsLast bool         `json:"isLast"`
			}
			if err := c.Do(ctx, http.MethodGet, "project/search", url.Values{"startAt": {strconv.Itoa(start)}, "maxResults": {"100"}}, nil, &page); err != nil {
				return nil, err
			}
			list = append(list, page.Values...)
			if page.IsLast || len(page.Values) == 0 {
				break
			}
			start += len(page.Values)
		}
	} else if err := c.Do(ctx, http.MethodGet, "project", nil, nil, &list); err != nil {
		return nil, err
	}
	out := make([]tracker.Project, 0, len(list))
	for _, x := range list {
		if x.Key != "" && !x.Archived {
			out = append(out, tracker.Project{Key: x.Key, Name: x.Name, Description: x.Description, Logo: x.Avatars["48x48"]})
		}
	}
	return out, nil
}

// ---- Confluence ----

type apiPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Space *struct {
		Key string `json:"key"`
	} `json:"space"`
	Version *struct {
		When string `json:"when"`
	} `json:"version"`
	Body *struct {
		Storage *struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Ancestors []struct {
		ID string `json:"id"`
	} `json:"ancestors"`
	Children *struct {
		Page *struct {
			Size int `json:"size"`
		} `json:"page"`
	} `json:"children"`
	Links struct {
		WebUI string `json:"webui"`
		Base  string `json:"base"`
	} `json:"_links"`
}

func article(a apiPage, web string) tracker.Article {
	out := tracker.Article{ID: a.ID, Title: a.Title}
	base := web
	if a.Links.Base != "" {
		base = a.Links.Base
	}
	if a.Links.WebUI != "" {
		out.URL = strings.TrimRight(base, "/") + a.Links.WebUI
	}
	if a.Space != nil {
		out.Project = a.Space.Key
	}
	if a.Version != nil {
		out.UpdatedAt = jiraTime(a.Version.When)
	}
	if a.Body != nil && a.Body.Storage != nil {
		out.Content = tracker.HTMLText(a.Body.Storage.Value)
	}
	if n := len(a.Ancestors); n > 0 {
		out.ParentID = a.Ancestors[n-1].ID
	}
	out.HasChildren = a.Children != nil && a.Children.Page != nil && a.Children.Page.Size > 0
	return out
}

// LooksLikeCQL says whether a query is CQL already, rather than words to look for.
func LooksLikeCQL(q string) bool {
	low := " " + strings.ToLower(q) + " "
	for _, op := range []string{"~", "=", " and ", " or ", " order by ", " in ("} {
		if strings.Contains(low, op) {
			return true
		}
	}
	return false
}

// CQL is the search a Knowledge query becomes: pages, narrowed by base, matching the query's words or the query as CQL.
func CQL(query, base string) string {
	parts := []string{"type = page"}
	if b := strings.TrimSpace(base); b != "" {
		parts = append(parts, "("+b+")")
	}
	if q := strings.TrimSpace(query); q != "" {
		if LooksLikeCQL(q) {
			parts = append(parts, "("+q+")")
		} else {
			parts = append(parts, `text ~ "`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(q)+`"`)
		}
	}
	return strings.Join(parts, " AND ")
}

// SearchArticles implements tracker.KnowledgeProvider.
func (p *Provider) SearchArticles(ctx context.Context, query, base string, limit int) ([]tracker.Article, error) {
	c, web, err := p.wiki(ctx)
	if err != nil {
		return nil, err
	}
	var res struct {
		Results []apiPage `json:"results"`
	}
	q := url.Values{"cql": {CQL(query, base)}, "limit": {strconv.Itoa(max(limit, 1))}, "expand": {"body.storage,space,version"}}
	if err := c.Do(ctx, http.MethodGet, "content/search", q, nil, &res); err != nil {
		return nil, err
	}
	out := make([]tracker.Article, 0, len(res.Results))
	for _, a := range res.Results {
		out = append(out, article(a, web))
	}
	return out, nil
}

// ListArticles implements tracker.KnowledgeProvider. The top level is the pages the narrowing matches, by title; a page's children
// open below it.
func (p *Provider) ListArticles(ctx context.Context, parentID, base string, page tracker.Page) ([]tracker.Article, bool, error) {
	c, web, err := p.wiki(ctx)
	if err != nil {
		return nil, false, err
	}
	cql := CQL("", base) + " order by title"
	if parentID != "" {
		cql = "type = page AND parent = " + strconv.Quote(parentID) + " order by title"
	}
	var res struct {
		Results []apiPage `json:"results"`
	}
	q := url.Values{"cql": {cql}, "start": {strconv.Itoa(page.Offset)}, "limit": {strconv.Itoa(page.Limit + 1)}, "expand": {"space,version,children.page"}}
	if err := c.Do(ctx, http.MethodGet, "content/search", q, nil, &res); err != nil {
		return nil, false, err
	}
	more := len(res.Results) > page.Limit
	if more {
		res.Results = res.Results[:page.Limit]
	}
	out := make([]tracker.Article, 0, len(res.Results))
	for _, a := range res.Results {
		out = append(out, article(a, web))
	}
	return out, more, nil
}

// GetArticle implements tracker.KnowledgeProvider.
func (p *Provider) GetArticle(ctx context.Context, id string) (*tracker.Article, error) {
	c, web, err := p.wiki(ctx)
	if err != nil {
		return nil, err
	}
	var a apiPage
	if err := c.Do(ctx, http.MethodGet, "content/"+url.PathEscape(id), url.Values{"expand": {"body.storage,space,version,ancestors,children.page"}}, nil, &a); err != nil {
		return nil, err
	}
	out := article(a, web)
	return &out, nil
}
