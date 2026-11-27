// Package linear is the Linear tracker provider, over its GraphQL API, and "linear-docs", Linear's documents as a knowledge
// provider that borrows a Linear connection's sign-in (docs.go). docs/design/tracker.md, "Writing a provider". The server mirrors
// it in packages/core/src/tracker/linear.ts and packages/core/src/knowledge/linear-docs.ts.
//
// Sign-in, checked against Linear's documentation (https://linear.app/developers/graphql#authentication,
// https://linear.app/developers/oauth-2-0-authentication): a personal API key in the Authorization header as it is, or OAuth 2.0
// with PKCE, whose access token goes as a bearer token.
package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/xlsft/harness/packages/client/tracker"
)

// Name is this provider's name in a connection; DocsName the documents' knowledge provider's.
const (
	Name     = "linear"
	DocsName = "linear-docs"
)

// DefaultBaseURL is Linear's API.
const DefaultBaseURL = "https://api.linear.app"

func init() {
	tracker.Register(Name, tracker.Factory{
		Capabilities: Capabilities(),
		Open:         Open,
		OAuth: func(baseURL string) (tracker.OAuthExchange, error) {
			return oauthExchange{api: apiBase(baseURL)}, nil
		},
	})
	tracker.RegisterKnowledge(DocsName, tracker.KnowledgeFactory{
		Capabilities: DocsCapabilities(),
		Open: func(o tracker.KnowledgeOpenOptions) (tracker.KnowledgeProvider, error) {
			p, err := Open(tracker.OpenOptions{BaseURL: o.BaseURL, Auth: o.Auth, Login: o.Login, OAuthClientID: o.OAuthClientID, Credentials: o.Credentials, Options: o.Options, OnTokens: o.OnTokens})
			if err != nil {
				return nil, err
			}
			return p.(*Provider), nil
		},
	})
}

// DocsCapabilities describes Linear's documents as a knowledge source: it has no sign-in of its own.
func DocsCapabilities() tracker.KnowledgeCapabilities {
	return tracker.KnowledgeCapabilities{
		Provider: DocsName, DisplayName: "Linear documents",
		Description:    "Documents of a Linear workspace, read live, optionally of one project.",
		DocsURL:        "https://linear.app/docs/project-documents",
		Logo:           "linear",
		BorrowFrom:     []string{Name},
		QueryLanguage:  "Linear project",
		QueryExample:   "project: Handbook",
		DefaultBaseURL: DefaultBaseURL,
	}
}

// Capabilities describes the provider.
func Capabilities() tracker.Capabilities {
	return tracker.Capabilities{
		Provider: Name, DisplayName: "Linear",
		AuthMethods:   []tracker.AuthMethod{tracker.AuthToken, tracker.AuthOAuth},
		QueryLanguage: "Linear filter", QueryExample: `team:ENG state:"Todo" label:Bug`,
		CustomFields: false, Comments: true, DefaultBaseURL: DefaultBaseURL,
		DocsURL:            "https://linear.app/docs/api-and-webhooks",
		Logo:               "linear",
		BaseURLPlaceholder: DefaultBaseURL,
		AuthHints: map[tracker.AuthMethod]tracker.AuthHint{
			tracker.AuthToken: {Help: "A personal API key from Linear → Settings → Security & access → Personal API keys.", SecretLabel: "API key"},
			tracker.AuthOAuth: {Help: "An OAuth 2.0 application from Linear → Settings → API → OAuth applications."},
		},
	}
}

func apiBase(raw string) string {
	b := strings.TrimRight(tracker.NormalizeURL(raw), "/")
	if b == "" {
		return DefaultBaseURL
	}
	return strings.TrimSuffix(b, "/graphql")
}

// authorizeBase: Linear's consent page is on linear.app, its API on api.linear.app; anything else (a fake) serves both.
func authorizeBase(api string) string {
	if api == DefaultBaseURL {
		return "https://linear.app"
	}
	return api
}

type oauthExchange struct{ api string }

func (o oauthExchange) AuthorizeURL(_ context.Context, clientID, redirectURI, state, challenge string) (string, error) {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirectURI}, "state": {state}, "response_type": {"code"}, "scope": {"read,write"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "prompt": {"consent"}}
	return authorizeBase(o.api) + "/oauth/authorize?" + q.Encode(), nil
}

func (o oauthExchange) Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI, verifier string) (tracker.Credentials, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "redirect_uri": {redirectURI}, "code_verifier": {verifier}}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	return tracker.OAuthTokens(ctx, nil, o.api+"/oauth/token", form, false)
}

// Provider implements tracker.Provider.
type Provider struct {
	api *tracker.JSONClient
	web string
}

var _ tracker.Provider = (*Provider)(nil)

// Open makes a provider for a connection.
func Open(o tracker.OpenOptions) (tracker.Provider, error) {
	base := apiBase(o.BaseURL)
	p := &Provider{web: "https://linear.app"}
	c := &tracker.JSONClient{Base: base, HTTP: tracker.NewHTTPClient(), Name: "Linear"}
	switch o.Auth {
	case tracker.AuthToken:
		if o.Credentials.Secret == "" {
			return nil, fmt.Errorf("%w: no API key is stored for this connection", tracker.ErrAuth)
		}
		key := o.Credentials.Secret
		// A personal API key goes as it is; an OAuth token pasted here as a bearer one.
		c.Authorize = func(_ context.Context, h http.Header) error { h.Set("Authorization", key); return nil }
	case tracker.AuthOAuth:
		ts := &tracker.TokenSource{Creds: o.Credentials, OnTokens: o.OnTokens, Refresh: func(ctx context.Context, refresh string) (tracker.Credentials, error) {
			form := url.Values{"grant_type": {"refresh_token"}, "client_id": {o.OAuthClientID}, "refresh_token": {refresh}}
			if o.Credentials.ClientSecret != "" {
				form.Set("client_secret", o.Credentials.ClientSecret)
			}
			return tracker.OAuthTokens(ctx, c.HTTP, base+"/oauth/token", form, false)
		}}
		c.Authorize = func(ctx context.Context, h http.Header) error {
			t, err := ts.Token(ctx)
			if err != nil {
				return err
			}
			h.Set("Authorization", "Bearer "+t)
			return nil
		}
	default:
		return nil, fmt.Errorf("Linear does not sign in with %q", o.Auth)
	}
	p.api = c
	return p, nil
}

// gqlError is what Linear says went wrong. It answers 200 or 400 with errors, and names authentication failures in extensions.
type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"extensions"`
}

func (p *Provider) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	err := p.api.Do(ctx, http.MethodPost, "graphql", nil, map[string]any{"query": query, "variables": vars}, &res)
	var he *tracker.HTTPError
	if errors.As(err, &he) && he.Status == http.StatusBadRequest {
		// The body says why; the first error is the reason.
		_ = json.Unmarshal([]byte(he.Body), &res)
		if len(res.Errors) == 0 {
			return err
		}
	} else if err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		e := res.Errors[0]
		msg := fmt.Errorf("Linear: %s", e.Message)
		switch strings.ToUpper(e.Extensions.Code) {
		case "AUTHENTICATION_ERROR":
			return fmt.Errorf("%w: %v", tracker.ErrAuth, msg)
		case "FORBIDDEN":
			return fmt.Errorf("%w: %v", tracker.ErrForbidden, msg)
		case "RATELIMITED":
			return fmt.Errorf("%w: %v", tracker.ErrTransient, msg)
		}
		if strings.Contains(strings.ToLower(e.Message), "not found") || strings.EqualFold(e.Extensions.Type, "invalid input") && strings.Contains(strings.ToLower(e.Message), "entity") {
			return fmt.Errorf("%w: %v", tracker.ErrNotFound, msg)
		}
		return msg
	}
	if out != nil {
		return json.Unmarshal(res.Data, out)
	}
	return nil
}

// Capabilities implements tracker.Provider.
func (*Provider) Capabilities() tracker.Capabilities { return Capabilities() }

// Test implements tracker.Provider.
func (p *Provider) Test(ctx context.Context) (tracker.Identity, error) {
	var d struct {
		Viewer struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Email       string `json:"email"`
		} `json:"viewer"`
	}
	if err := p.gql(ctx, `query { viewer { name displayName email } }`, nil, &d); err != nil {
		return tracker.Identity{}, err
	}
	login := d.Viewer.Email
	if login == "" {
		login = d.Viewer.DisplayName
	}
	return tracker.Identity{Login: login, DisplayName: d.Viewer.Name}, nil
}

type apiIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updatedAt"`
	Priority    int    `json:"priority"`
	Team        *struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
	State *struct {
		Name string `json:"name"`
	} `json:"state"`
	Assignee *struct {
		Name string `json:"name"`
	} `json:"assignee"`
	Project *struct {
		Name string `json:"name"`
	} `json:"project"`
	Labels *struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Comments *struct {
		Nodes []struct {
			Body      string `json:"body"`
			CreatedAt string `json:"createdAt"`
			User      *struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"nodes"`
	} `json:"comments"`
}

// PriorityNames are Linear's priorities by number.
var PriorityNames = []string{"No priority", "Urgent", "High", "Medium", "Low"}

func (i apiIssue) toIssue() tracker.Issue {
	out := tracker.Issue{ID: i.Identifier, Title: i.Title, URL: i.URL, Description: i.Description, UpdatedAt: i.UpdatedAt}
	if i.Team != nil {
		out.Project = i.Team.Key
	}
	add := func(name, v string) {
		if v != "" {
			out.Fields = append(out.Fields, tracker.FieldValue{Name: name, Value: v})
		}
	}
	if i.State != nil {
		add("State", i.State.Name)
	}
	if i.Priority >= 0 && i.Priority < len(PriorityNames) && i.Priority > 0 {
		add("Priority", PriorityNames[i.Priority])
	}
	if i.Assignee != nil {
		add("Assignee", i.Assignee.Name)
	}
	if i.Project != nil {
		add("Project", i.Project.Name)
	}
	if i.Labels != nil {
		var names []string
		for _, l := range i.Labels.Nodes {
			names = append(names, l.Name)
		}
		add("Labels", strings.Join(names, ", "))
	}
	if i.Comments != nil {
		for _, c := range i.Comments.Nodes {
			author := ""
			if c.User != nil {
				author = c.User.Name
			}
			out.Comments = append(out.Comments, tracker.IssueComment{Author: author, Text: c.Body, CreatedAt: c.CreatedAt})
		}
	}
	return out
}

const listSelection = `identifier title url updatedAt team { key }`

// Search implements tracker.Provider. The query is an IssueFilter as JSON, or the short form ParseFilter reads.
func (p *Provider) Search(ctx context.Context, query string, page tracker.Page) ([]tracker.Issue, bool, error) {
	filter, err := ParseFilter(query)
	if err != nil {
		return nil, false, err
	}
	limit := max(page.Limit, 1)
	skip, after := page.Offset, ""
	var out []tracker.Issue
	for {
		vars := map[string]any{"filter": filter, "first": min(100, skip+limit-len(out)+1)}
		if after != "" {
			vars["after"] = after
		}
		var d struct {
			Issues struct {
				Nodes    []apiIssue `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}
		q := `query($filter: IssueFilter, $first: Int, $after: String) { issues(filter: $filter, first: $first, after: $after, orderBy: createdAt) { nodes { ` + listSelection + ` } pageInfo { hasNextPage endCursor } } }`
		if err := p.gql(ctx, q, vars, &d); err != nil {
			return nil, false, err
		}
		for _, i := range d.Issues.Nodes {
			if skip > 0 {
				skip--
				continue
			}
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, i.toIssue())
		}
		if !d.Issues.PageInfo.HasNextPage || len(d.Issues.Nodes) == 0 {
			return out, false, nil
		}
		after = d.Issues.PageInfo.EndCursor
	}
}

// ParseFilter reads a stage input's query: an IssueFilter as JSON (`{"team":{"key":{"eq":"ENG"}}}`), or the short form
// `team:ENG state:"In Progress" assignee:me label:Bug priority:urgent project:"Q3 launch" cycle:current` with other words matched
// against the title. Keys may repeat: values of one key are alternatives.
func ParseFilter(q string) (map[string]any, error) {
	q = strings.TrimSpace(q)
	if strings.HasPrefix(q, "{") {
		var f map[string]any
		if err := json.Unmarshal([]byte(q), &f); err != nil {
			return nil, fmt.Errorf("the filter is not valid JSON: %w", err)
		}
		return f, nil
	}
	values := map[string][]string{}
	var order []string
	var words []string
	for _, tok := range tokens(q) {
		k, v, ok := strings.Cut(tok, ":")
		if !ok || k == "" {
			words = append(words, tok)
			continue
		}
		k = strings.ToLower(k)
		if _, seen := values[k]; !seen {
			order = append(order, k)
		}
		values[k] = append(values[k], strings.Trim(v, `"`))
	}
	var and []any
	for _, k := range order {
		vs := values[k]
		var alts []any
		for _, v := range vs {
			f, err := keyFilter(k, v)
			if err != nil {
				return nil, err
			}
			alts = append(alts, f)
		}
		if len(alts) == 1 {
			and = append(and, alts[0])
		} else {
			and = append(and, map[string]any{"or": alts})
		}
	}
	for _, w := range words {
		and = append(and, map[string]any{"title": map[string]any{"containsIgnoreCase": strings.Trim(w, `"`)}})
	}
	switch len(and) {
	case 0:
		return map[string]any{}, nil
	case 1:
		return and[0].(map[string]any), nil
	}
	return map[string]any{"and": and}, nil
}

func keyFilter(k, v string) (map[string]any, error) {
	eq := func(field string) map[string]any { return map[string]any{field: map[string]any{"eqIgnoreCase": v}} }
	switch k {
	case "team":
		return map[string]any{"team": map[string]any{"or": []any{map[string]any{"key": map[string]any{"eqIgnoreCase": v}}, eq("name")}}}, nil
	case "state", "status":
		return map[string]any{"state": map[string]any{"or": []any{eq("name"), map[string]any{"type": map[string]any{"eq": strings.ToLower(v)}}}}}, nil
	case "assignee":
		switch strings.ToLower(v) {
		case "me":
			return map[string]any{"assignee": map[string]any{"isMe": map[string]any{"eq": true}}}, nil
		case "none", "unassigned":
			return map[string]any{"assignee": map[string]any{"null": true}}, nil
		}
		return map[string]any{"assignee": map[string]any{"or": []any{map[string]any{"email": map[string]any{"eqIgnoreCase": v}}, eq("name"), eq("displayName")}}}, nil
	case "label", "labels":
		return map[string]any{"labels": map[string]any{"some": eq("name")}}, nil
	case "project":
		return map[string]any{"project": eq("name")}, nil
	case "priority":
		n, err := priorityNumber(v)
		if err != nil {
			return nil, err
		}
		return map[string]any{"priority": map[string]any{"eq": n}}, nil
	case "cycle":
		if strings.EqualFold(v, "current") || strings.EqualFold(v, "active") {
			return map[string]any{"cycle": map[string]any{"isActive": map[string]any{"eq": true}}}, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("cycle: %q is not current or a number", v)
		}
		return map[string]any{"cycle": map[string]any{"number": map[string]any{"eq": n}}}, nil
	}
	return nil, fmt.Errorf("unknown filter %q: use team, state, assignee, label, project, priority or cycle, or an IssueFilter as JSON", k)
}

func priorityNumber(v string) (int, error) {
	if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 4 {
		return n, nil
	}
	for i, name := range PriorityNames {
		if strings.EqualFold(name, v) || (i == 0 && (strings.EqualFold(v, "none") || strings.EqualFold(v, "no"))) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("priority: %q is not 0 to 4 or one of %s", v, strings.Join(PriorityNames, ", "))
}

// tokens splits on spaces outside quotes, keeping the quotes for the caller to trim.
func tokens(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			b.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n') && !quoted:
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

const fullSelection = `id identifier title url description updatedAt priority team { id key } state { name } assignee { name } project { name } labels { nodes { id name } } comments(last: 10) { nodes { body createdAt user { name } } }`

func (p *Provider) issue(ctx context.Context, id string) (*apiIssue, error) {
	var d struct {
		Issue *apiIssue `json:"issue"`
	}
	if err := p.gql(ctx, `query($id: String!) { issue(id: $id) { `+fullSelection+` } }`, map[string]any{"id": id}, &d); err != nil {
		return nil, err
	}
	if d.Issue == nil {
		return nil, fmt.Errorf("%w: issue %s", tracker.ErrNotFound, id)
	}
	return d.Issue, nil
}

// Get implements tracker.Provider.
func (p *Provider) Get(ctx context.Context, id string) (*tracker.Issue, error) {
	i, err := p.issue(ctx, id)
	if err != nil {
		return nil, err
	}
	out := i.toIssue()
	return &out, nil
}

type team struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Name   string `json:"name"`
	States struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"states"`
	Labels struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Members struct {
		Nodes []member `json:"nodes"`
	} `json:"members"`
}

type member struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

func (p *Provider) team(ctx context.Context, idOrKey string) (*team, error) {
	var d struct {
		Teams struct {
			Nodes []team `json:"nodes"`
		} `json:"teams"`
	}
	q := `query($f: TeamFilter) { teams(filter: $f, first: 2) { nodes { id key name states { nodes { id name } } labels(first: 250) { nodes { id name } } members(first: 250) { nodes { id name displayName email } } } } }`
	f := map[string]any{"or": []any{map[string]any{"key": map[string]any{"eqIgnoreCase": idOrKey}}, map[string]any{"id": map[string]any{"eq": idOrKey}}}}
	if err := p.gql(ctx, q, map[string]any{"f": f}, &d); err != nil {
		return nil, err
	}
	if len(d.Teams.Nodes) == 0 {
		return nil, fmt.Errorf("%w: no team %s this account can see", tracker.ErrNotFound, idOrKey)
	}
	return &d.Teams.Nodes[0], nil
}

// input shapes field values into an IssueUpdateInput for the issue's team.
func (p *Provider) input(ctx context.Context, t *team, current []string, fields []tracker.FieldValue) (map[string]any, error) {
	in := map[string]any{}
	labels := append([]string(nil), current...)
	labelsChanged := false
	labelID := func(name string) (string, error) {
		for _, l := range t.Labels.Nodes {
			if strings.EqualFold(l.Name, name) {
				return l.ID, nil
			}
		}
		return "", fmt.Errorf("no label %q in %s", name, t.Key)
	}
	for _, f := range fields {
		name := strings.ToLower(strings.TrimSpace(f.Name))
		switch name {
		case "state", "status", "stage":
			var names []string
			for _, s := range t.States.Nodes {
				if strings.EqualFold(s.Name, f.Value) {
					in["stateId"] = s.ID
				}
				names = append(names, s.Name)
			}
			if in["stateId"] == nil {
				return nil, fmt.Errorf("%q is not a state of %s; its states: %s", f.Value, t.Key, strings.Join(names, ", "))
			}
		case "priority":
			n, err := priorityNumber(f.Value)
			if err != nil {
				return nil, err
			}
			in["priority"] = n
		case "assignee":
			v := strings.TrimSpace(f.Value)
			switch strings.ToLower(v) {
			case "", "none", "unassigned":
				in["assigneeId"] = nil
				continue
			case "me":
				var d struct {
					Viewer struct {
						ID string `json:"id"`
					} `json:"viewer"`
				}
				if err := p.gql(ctx, `query { viewer { id } }`, nil, &d); err != nil {
					return nil, err
				}
				in["assigneeId"] = d.Viewer.ID
				continue
			}
			for _, m := range t.Members.Nodes {
				if strings.EqualFold(m.Email, v) || strings.EqualFold(m.Name, v) || strings.EqualFold(m.DisplayName, v) {
					in["assigneeId"] = m.ID
				}
			}
			if _, ok := in["assigneeId"]; !ok {
				return nil, fmt.Errorf("no member of %s is %q", t.Key, v)
			}
		case "labels", "add labels", "remove labels":
			if name == "labels" {
				labels, labelsChanged = nil, true
			}
			for _, l := range splitList(f.Value) {
				id, err := labelID(l)
				if err != nil {
					return nil, err
				}
				labelsChanged = true
				if name == "remove labels" {
					labels = remove(labels, id)
				} else if !contains(labels, id) {
					labels = append(labels, id)
				}
			}
		case "title":
			in["title"] = f.Value
		case "description":
			in["description"] = f.Value
		default:
			return nil, fmt.Errorf("unknown field %q: Linear sets State, Priority, Assignee, Labels, Add labels, Remove labels, Title and Description", f.Name)
		}
	}
	if labelsChanged {
		in["labelIds"] = labels
	}
	return in, nil
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

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func remove(list []string, s string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func (p *Provider) mutate(ctx context.Context, q string, vars map[string]any, field string) (json.RawMessage, error) {
	var d map[string]struct {
		Success bool            `json:"success"`
		Issue   json.RawMessage `json:"issue"`
	}
	if err := p.gql(ctx, q, vars, &d); err != nil {
		return nil, err
	}
	if r, ok := d[field]; !ok || !r.Success {
		return nil, fmt.Errorf("Linear did not accept %s", field)
	} else {
		return r.Issue, nil
	}
}

// Create implements tracker.Provider. The project is a team key.
func (p *Provider) Create(ctx context.Context, draft tracker.IssueDraft) (*tracker.Issue, error) {
	t, err := p.team(ctx, draft.Project)
	if err != nil {
		return nil, err
	}
	in, err := p.input(ctx, t, nil, draft.Fields)
	if err != nil {
		return nil, err
	}
	in["teamId"], in["title"] = t.ID, draft.Title
	if draft.Description != "" {
		in["description"] = draft.Description
	}
	raw, err := p.mutate(ctx, `mutation($in: IssueCreateInput!) { issueCreate(input: $in) { success issue { `+listSelection+` } } }`, map[string]any{"in": in}, "issueCreate")
	if err != nil {
		return nil, err
	}
	var i apiIssue
	if err := json.Unmarshal(raw, &i); err != nil {
		return nil, err
	}
	out := i.toIssue()
	return &out, nil
}

// Update implements tracker.Provider.
func (p *Provider) Update(ctx context.Context, id string, u tracker.IssueUpdate) error {
	cur, err := p.issue(ctx, id)
	if err != nil {
		return err
	}
	fields := append([]tracker.FieldValue(nil), u.Fields...)
	if u.Title != nil {
		fields = append(fields, tracker.FieldValue{Name: "Title", Value: *u.Title})
	}
	if u.Description != nil {
		fields = append(fields, tracker.FieldValue{Name: "Description", Value: *u.Description})
	}
	if len(fields) == 0 {
		return nil
	}
	if cur.Team == nil {
		return errors.New("Linear did not say which team the issue is in")
	}
	t, err := p.team(ctx, cur.Team.ID)
	if err != nil {
		return err
	}
	var current []string
	if cur.Labels != nil {
		for _, l := range cur.Labels.Nodes {
			current = append(current, l.ID)
		}
	}
	in, err := p.input(ctx, t, current, fields)
	if err != nil {
		return err
	}
	_, err = p.mutate(ctx, `mutation($id: String!, $in: IssueUpdateInput!) { issueUpdate(id: $id, input: $in) { success } }`, map[string]any{"id": cur.ID, "in": in}, "issueUpdate")
	return err
}

// Comment implements tracker.Provider.
func (p *Provider) Comment(ctx context.Context, id, body string) error {
	cur, err := p.issue(ctx, id)
	if err != nil {
		return err
	}
	_, err = p.mutate(ctx, `mutation($in: CommentCreateInput!) { commentCreate(input: $in) { success } }`, map[string]any{"in": map[string]any{"issueId": cur.ID, "body": body}}, "commentCreate")
	return err
}

// Fields implements tracker.Provider: the project is a team key.
func (p *Provider) Fields(ctx context.Context, project string) ([]tracker.Field, error) {
	state := tracker.Field{Name: "State", Kind: "state"}
	assignee := tracker.Field{Name: "Assignee", Kind: "user"}
	labels := tracker.Field{Name: "Labels", Kind: "labels", Multi: true}
	if project != "" {
		t, err := p.team(ctx, project)
		if err != nil {
			return nil, err
		}
		for _, s := range t.States.Nodes {
			state.Values = append(state.Values, s.Name)
		}
		for _, m := range t.Members.Nodes {
			v := m.Email
			if v == "" {
				v = m.Name
			}
			assignee.Values = append(assignee.Values, v)
		}
		for _, l := range t.Labels.Nodes {
			labels.Values = append(labels.Values, l.Name)
		}
	}
	add, rm := labels, labels
	add.Name, rm.Name = "Add labels", "Remove labels"
	return []tracker.Field{state, {Name: "Priority", Kind: "bundle", Values: PriorityNames}, assignee, labels, add, rm}, nil
}

// Projects implements tracker.Provider: Linear's teams, by key.
func (p *Provider) Projects(ctx context.Context) ([]tracker.Project, error) {
	var d struct {
		Teams struct {
			Nodes []struct {
				Key         string `json:"key"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Icon        string `json:"icon"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := p.gql(ctx, `query { teams(first: 250) { nodes { key name description icon } } }`, nil, &d); err != nil {
		return nil, err
	}
	out := make([]tracker.Project, 0, len(d.Teams.Nodes))
	for _, t := range d.Teams.Nodes {
		out = append(out, tracker.Project{Key: t.Key, Name: t.Name, Description: t.Description})
	}
	return out, nil
}
