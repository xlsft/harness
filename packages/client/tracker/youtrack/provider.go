package youtrack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// The provider over Client. How it works without admin rights comes from xlsft/ymcp: field schemas fall back to what recent
// issues say, groups to Hub, and deleted comments stay out of reads.

// Name is this provider's name in a connection.
const Name = "youtrack"

const (
	issueListFields  = "idReadable,summary,updated,project(shortName)"
	issueFullFields  = "idReadable,summary,description,updated,project(id,shortName,name),customFields(name,value(name,login,fullName,text,presentation))"
	articleFields    = "idReadable,summary,content,updated,project(shortName),parentArticle(idReadable),childArticles(idReadable)"
	commentFields    = "text,created,deleted,author(login,fullName)"
	projectFieldList = "$type,canBeEmpty,emptyFieldText,field(name,fieldType(id,isMultiValue,valueType)),bundle(values(name,archived),aggregatedUsers(login))"
	// schemaTTL is how long a project's fields are reused: a bulk of outputs costs one schema request.
	schemaTTL = 5 * time.Minute
)

func init() {
	tracker.Register(Name, tracker.Factory{
		Capabilities: Capabilities(),
		Open:         Open,
		OAuth: func(baseURL string) (tracker.OAuthExchange, error) {
			c, err := New(Options{BaseURL: baseURL, Token: "unused"})
			if err != nil {
				return nil, err
			}
			return oauthExchange{c}, nil
		},
	})
}

// Capabilities describes the provider.
func Capabilities() tracker.Capabilities {
	return tracker.Capabilities{
		Provider:      Name,
		DisplayName:   "YouTrack",
		AuthMethods:   []tracker.AuthMethod{tracker.AuthToken, tracker.AuthOAuth, tracker.AuthPassword},
		QueryLanguage: "YouTrack search",
		QueryExample:  "project: DEMO Stage: Backlog",
		CustomFields:  true,
		Comments:      true,
		Knowledge:     true,
	}
}

type oauthExchange struct{ c *Client }

func (o oauthExchange) AuthorizeURL(ctx context.Context, clientID, redirectURI, state, challenge string) (string, error) {
	u, err := o.c.AuthorizeURL(ctx, clientID, redirectURI, state, challenge)
	return u, classify(err)
}

func (o oauthExchange) Exchange(ctx context.Context, clientID, clientSecret, code, redirectURI, verifier string) (tracker.Credentials, error) {
	access, refresh, expires, err := o.c.ExchangeCode(ctx, clientID, clientSecret, code, redirectURI, verifier)
	if err != nil {
		return tracker.Credentials{}, classify(err)
	}
	return tracker.Credentials{AccessToken: access, RefreshToken: refresh, ExpiresAt: expires.Unix()}, nil
}

// Provider implements tracker.Provider and tracker.KnowledgeProvider.
type Provider struct {
	c   *Client
	now func() time.Time

	mu      sync.Mutex
	schemas map[string]cachedSchema
}

var (
	_ tracker.Provider          = (*Provider)(nil)
	_ tracker.KnowledgeProvider = (*Provider)(nil)
)

type cachedSchema struct {
	fields map[string]projectField
	at     time.Time
}

// Open makes a provider for a connection.
func Open(o tracker.OpenOptions) (tracker.Provider, error) {
	opts := Options{BaseURL: o.BaseURL, Proxy: ""}
	switch o.Auth {
	case tracker.AuthToken:
		opts.Token = o.Credentials.Secret
		if opts.Token == "" {
			return nil, fmt.Errorf("%w: no token is stored for this connection", tracker.ErrAuth)
		}
	case tracker.AuthPassword:
		opts.Login, opts.Password = o.Login, o.Credentials.Secret
	case tracker.AuthOAuth:
		var exp time.Time
		if o.Credentials.ExpiresAt > 0 {
			exp = time.Unix(o.Credentials.ExpiresAt, 0)
		}
		opts.OAuth = &OAuthOptions{ClientID: o.OAuthClientID, ClientSecret: o.Credentials.ClientSecret, AccessToken: o.Credentials.AccessToken, RefreshToken: o.Credentials.RefreshToken, Expires: exp}
		if o.OnTokens != nil {
			base := o.Credentials
			opts.OAuth.OnTokens = func(access, refresh string, expires time.Time) {
				base.AccessToken, base.RefreshToken, base.ExpiresAt = access, refresh, expires.Unix()
				o.OnTokens(base)
			}
		}
	default:
		return nil, fmt.Errorf("unknown auth method %q", o.Auth)
	}
	c, err := New(opts)
	if err != nil {
		return nil, err
	}
	return &Provider{c: c, now: time.Now, schemas: map[string]cachedSchema{}}, nil
}

// classify turns client errors into the tracker package's kinds, keeping the message.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotAuthenticated) {
		return fmt.Errorf("%w: %v", tracker.ErrAuth, err)
	}
	var e *Error
	if errors.As(err, &e) {
		switch {
		case e.Status == http.StatusNotFound:
			return fmt.Errorf("%w: %v", tracker.ErrNotFound, err)
		case e.Status == http.StatusUnauthorized:
			return fmt.Errorf("%w: %v", tracker.ErrAuth, err)
		case e.Status == http.StatusForbidden:
			return fmt.Errorf("%w: %v", tracker.ErrForbidden, err)
		case e.Status == http.StatusTooManyRequests || e.Status >= 500:
			return fmt.Errorf("%w: %v", tracker.ErrTransient, err)
		}
		return err
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", tracker.ErrTransient, err)
	}
	return err
}

func isForbidden(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusForbidden
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Capabilities implements tracker.Provider.
func (*Provider) Capabilities() tracker.Capabilities { return Capabilities() }

// Test implements tracker.Provider.
func (p *Provider) Test(ctx context.Context) (tracker.Identity, error) {
	var me struct {
		Login    string `json:"login"`
		FullName string `json:"fullName"`
	}
	if err := p.c.Get(ctx, "users/me", Fields("login,fullName"), &me); err != nil {
		return tracker.Identity{}, classify(err)
	}
	return tracker.Identity{Login: me.Login, DisplayName: me.FullName}, nil
}

type apiProject struct {
	ID        string `json:"id"`
	ShortName string `json:"shortName"`
	Name      string `json:"name"`
}

type apiCustomField struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

type apiIssue struct {
	IDReadable   string           `json:"idReadable"`
	Summary      string           `json:"summary"`
	Description  string           `json:"description"`
	Updated      int64            `json:"updated"`
	Project      apiProject       `json:"project"`
	CustomFields []apiCustomField `json:"customFields"`
}

func isoMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// fieldText makes a custom field's value readable, whichever shape it has.
func fieldText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var out []string
		for _, r := range list {
			if s := fieldText(r); s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, ", ")
	}
	var o struct {
		Name         string `json:"name"`
		FullName     string `json:"fullName"`
		Login        string `json:"login"`
		Text         string `json:"text"`
		Presentation string `json:"presentation"`
	}
	if json.Unmarshal(raw, &o) == nil {
		for _, s := range []string{o.Name, o.FullName, o.Login, o.Text, o.Presentation} {
			if s != "" {
				return s
			}
		}
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func (p *Provider) issue(i apiIssue) tracker.Issue {
	out := tracker.Issue{ID: i.IDReadable, Title: i.Summary, URL: p.c.BaseURL() + "/issue/" + i.IDReadable, Description: i.Description, Project: i.Project.ShortName, UpdatedAt: isoMillis(i.Updated)}
	for _, f := range i.CustomFields {
		if v := fieldText(f.Value); v != "" {
			out.Fields = append(out.Fields, tracker.FieldValue{Name: f.Name, Value: v})
		}
	}
	return out
}

// Search implements tracker.Provider.
func (p *Provider) Search(ctx context.Context, query string, page tracker.Page) ([]tracker.Issue, bool, error) {
	q := url.Values{"query": {query}, "fields": {issueListFields}, "$skip": {strconv.Itoa(page.Offset)}, "$top": {strconv.Itoa(page.Limit + 1)}}
	var raw []apiIssue
	if err := p.c.Get(ctx, "issues", q, &raw); err != nil {
		return nil, false, classify(err)
	}
	hasMore := len(raw) > page.Limit
	if hasMore {
		raw = raw[:page.Limit]
	}
	out := make([]tracker.Issue, 0, len(raw))
	for _, i := range raw {
		out = append(out, p.issue(i))
	}
	return out, hasMore, nil
}

// Get implements tracker.Provider.
func (p *Provider) Get(ctx context.Context, id string) (*tracker.Issue, error) {
	path := "issues/" + url.PathEscape(id)
	var raw apiIssue
	if err := p.c.Get(ctx, path, Fields(issueFullFields), &raw); err != nil {
		return nil, classify(err)
	}
	out := p.issue(raw)
	// Deleted comments stay in the REST list, without text.
	var comments []struct {
		Text    string `json:"text"`
		Created int64  `json:"created"`
		Deleted bool   `json:"deleted"`
		Author  *struct {
			Login    string `json:"login"`
			FullName string `json:"fullName"`
		} `json:"author"`
	}
	if err := p.c.Get(ctx, path+"/comments", url.Values{"fields": {commentFields}, "$top": {"200"}}, &comments); err != nil {
		return nil, classify(err)
	}
	for _, c := range comments {
		if c.Deleted {
			continue
		}
		author := ""
		if c.Author != nil {
			author = c.Author.FullName
			if author == "" {
				author = c.Author.Login
			}
		}
		out.Comments = append(out.Comments, tracker.IssueComment{Author: author, Text: c.Text, CreatedAt: isoMillis(c.Created)})
	}
	if n := len(out.Comments); n > 10 {
		out.Comments = out.Comments[n-10:]
	}
	return &out, nil
}

func (p *Provider) projectByKey(ctx context.Context, key string) (apiProject, error) {
	var list []apiProject
	err := p.c.Get(ctx, "admin/projects", url.Values{"fields": {"id,shortName,name"}, "query": {key}, "$top": {"50"}}, &list)
	if err == nil {
		for _, x := range list {
			if strings.EqualFold(x.ShortName, key) {
				return x, nil
			}
		}
	} else if !isForbidden(err) && !isNotFound(err) {
		return apiProject{}, classify(err)
	}
	// A regular user may not read the path; the project's issues name it as well.
	var issues []apiIssue
	if err := p.c.Get(ctx, "issues", url.Values{"query": {"project: {" + key + "}"}, "fields": {"project(id,shortName,name)"}, "$top": {"1"}}, &issues); err != nil {
		return apiProject{}, classify(err)
	}
	if len(issues) == 0 || issues[0].Project.ID == "" {
		return apiProject{}, fmt.Errorf("%w: no project %s this account can see", tracker.ErrNotFound, key)
	}
	return issues[0].Project, nil
}

// Create implements tracker.Provider.
func (p *Provider) Create(ctx context.Context, d tracker.IssueDraft) (*tracker.Issue, error) {
	project, err := p.projectByKey(ctx, d.Project)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"project": map[string]any{"id": project.ID}, "summary": d.Title, "description": d.Description}
	if len(d.Fields) > 0 {
		cf, err := p.customFieldsFor(ctx, project, d.Fields)
		if err != nil {
			return nil, err
		}
		body["customFields"] = cf
	}
	var raw apiIssue
	if err := p.c.Post(ctx, "issues", Fields(issueListFields), body, &raw); err != nil {
		return nil, classify(err)
	}
	out := p.issue(raw)
	return &out, nil
}

// Update implements tracker.Provider.
func (p *Provider) Update(ctx context.Context, id string, u tracker.IssueUpdate) error {
	body := map[string]any{}
	if u.Title != nil {
		body["summary"] = *u.Title
	}
	if u.Description != nil {
		body["description"] = *u.Description
	}
	path := "issues/" + url.PathEscape(id)
	if len(u.Fields) > 0 {
		var raw apiIssue
		if err := p.c.Get(ctx, path, Fields("project(id,shortName)"), &raw); err != nil {
			return classify(err)
		}
		cf, err := p.customFieldsFor(ctx, raw.Project, u.Fields)
		if err != nil {
			return err
		}
		body["customFields"] = cf
	}
	return classify(p.c.Post(ctx, path, Fields("idReadable"), body, nil))
}

// Comment implements tracker.Provider.
func (p *Provider) Comment(ctx context.Context, id, text string) error {
	return classify(p.c.Post(ctx, "issues/"+url.PathEscape(id)+"/comments", Fields("id"), map[string]any{"text": text}, nil))
}

// Fields implements tracker.Provider.
func (p *Provider) Fields(ctx context.Context, project string) ([]tracker.Field, error) {
	if project == "" {
		return nil, nil
	}
	ref, err := p.projectByKey(ctx, project)
	if err != nil {
		return nil, err
	}
	schema, _, err := p.schema(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Field, 0, len(schema))
	for name, f := range schema {
		out = append(out, tracker.Field{Name: name, Kind: f.kind(), Multi: f.Field.FieldType.IsMultiValue, Values: f.values()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Projects implements tracker.Provider: what this account can read, despite the path's name.
func (p *Provider) Projects(ctx context.Context) ([]tracker.Project, error) {
	var list []struct {
		ShortName   string `json:"shortName"`
		Name        string `json:"name"`
		Description string `json:"description"`
		IconURL     string `json:"iconUrl"`
		Archived    bool   `json:"archived"`
	}
	if err := p.c.Get(ctx, "admin/projects", url.Values{"fields": {"shortName,name,description,iconUrl,archived"}, "$top": {"500"}}, &list); err != nil {
		return nil, classify(err)
	}
	out := make([]tracker.Project, 0, len(list))
	for _, x := range list {
		if x.ShortName == "" || x.Archived {
			continue
		}
		logo := x.IconURL
		if strings.HasPrefix(logo, "/") {
			logo = p.c.BaseURL() + logo
		}
		out = append(out, tracker.Project{Key: x.ShortName, Name: x.Name, Description: x.Description, Logo: logo})
	}
	return out, nil
}

// Group is a user group or project team.
type Group struct {
	ID   string
	Name string
}

// Groups lists groups. /api/groups takes admin rights, so for others it falls back to Hub; YouTrack 2026 keeps groups out of Hub,
// and then a regular user gets a clear error. No flow uses this yet: the sharing of outputs and comment visibility will.
func (p *Provider) Groups(ctx context.Context) ([]Group, error) {
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	err := p.c.Get(ctx, "groups", url.Values{"fields": {"id,name"}, "$top": {"-1"}}, &groups)
	if err == nil {
		out := make([]Group, 0, len(groups))
		for _, g := range groups {
			out = append(out, Group{ID: g.ID, Name: g.Name})
		}
		return out, nil
	}
	if !isForbidden(err) {
		return nil, classify(err)
	}
	var hub struct {
		UserGroups []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"usergroups"`
	}
	if hubErr := p.c.HubGet(ctx, "usergroups", url.Values{"fields": {"id,name"}, "$top": {"10000"}}, &hub); hubErr != nil {
		return nil, classify(fmt.Errorf("%w; Hub fallback: %v", err, hubErr))
	}
	if len(hub.UserGroups) == 0 {
		return nil, errors.New("this account can't list user groups: on this YouTrack only admins can")
	}
	out := make([]Group, 0, len(hub.UserGroups))
	for _, g := range hub.UserGroups {
		out = append(out, Group{ID: g.ID, Name: g.Name})
	}
	return out, nil
}

// ---- field schemas ----

type projectField struct {
	Type  string `json:"$type"`
	Field struct {
		Name      string `json:"name"`
		FieldType struct {
			ID           string `json:"id"`
			IsMultiValue bool   `json:"isMultiValue"`
			ValueType    string `json:"valueType"`
		} `json:"fieldType"`
	} `json:"field"`
	Bundle *struct {
		Values []struct {
			Name     string `json:"name"`
			Archived bool   `json:"archived"`
		} `json:"values"`
		AggregatedUsers []struct {
			Login string `json:"login"`
		} `json:"aggregatedUsers"`
	} `json:"bundle"`
}

func (f projectField) kind() string {
	switch f.Type {
	case "UserProjectCustomField":
		return "user"
	case "PeriodProjectCustomField":
		return "period"
	case "TextProjectCustomField":
		return "text"
	case "SimpleProjectCustomField":
		switch f.Field.FieldType.ValueType {
		case "date", "date and time":
			return "date"
		case "integer":
			return "integer"
		case "float":
			return "float"
		}
		return "string"
	}
	return "bundle"
}

func (f projectField) values() []string {
	if f.Bundle == nil {
		return nil
	}
	var out []string
	for _, v := range f.Bundle.Values {
		if !v.Archived {
			out = append(out, v.Name)
		}
	}
	return out
}

func (f projectField) issueFieldType() string {
	base := strings.TrimSuffix(f.Type, "ProjectCustomField")
	switch base {
	case "State", "Period", "Text":
		return base + "IssueCustomField"
	case "Simple":
		if f.kind() == "date" {
			return "DateIssueCustomField"
		}
		return "SimpleIssueCustomField"
	}
	if f.Field.FieldType.IsMultiValue {
		return "Multi" + base + "IssueCustomField"
	}
	return "Single" + base + "IssueCustomField"
}

func (f projectField) encodeOne(raw string) (any, error) {
	switch f.kind() {
	case "integer", "float", "date":
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		return n, nil
	case "user":
		if f.Bundle != nil && len(f.Bundle.AggregatedUsers) > 0 {
			logins := make([]string, 0, len(f.Bundle.AggregatedUsers))
			for _, u := range f.Bundle.AggregatedUsers {
				logins = append(logins, u.Login)
			}
			if !slices.Contains(logins, raw) {
				return nil, fmt.Errorf("user %q can't be set here", raw)
			}
		}
		return map[string]any{"login": raw}, nil
	case "period":
		return map[string]any{"$type": "PeriodValue", "presentation": raw}, nil
	case "text":
		return map[string]any{"$type": "TextFieldValue", "text": raw}, nil
	case "string":
		return raw, nil
	}
	if vals := f.values(); len(vals) > 0 && !slices.Contains(vals, raw) {
		return nil, fmt.Errorf("%q is not allowed; allowed: %s", raw, strings.Join(vals, ", "))
	}
	return map[string]any{"name": raw}, nil
}

func buildFields(schema map[string]projectField, values []tracker.FieldValue) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(values))
	for _, v := range values {
		f, ok := schema[v.Name]
		if !ok {
			names := make([]string, 0, len(schema))
			for n := range schema {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("unknown field %q; available: %s", v.Name, strings.Join(names, ", "))
		}
		var encoded any
		var err error
		if f.Field.FieldType.IsMultiValue {
			list := []any{}
			for _, part := range strings.Split(v.Value, ",") {
				one, e := f.encodeOne(strings.TrimSpace(part))
				if e != nil {
					err = e
					break
				}
				list = append(list, one)
			}
			encoded = list
		} else {
			encoded, err = f.encodeOne(v.Value)
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", v.Name, err)
		}
		out = append(out, map[string]any{"name": v.Name, "$type": f.issueFieldType(), "value": encoded})
	}
	return out, nil
}

// customFieldsFor encodes values against the project's fields. A cached schema that rejects them may be out of date, so they are
// checked once more against a fresh one.
func (p *Provider) customFieldsFor(ctx context.Context, project apiProject, values []tracker.FieldValue) ([]map[string]any, error) {
	schema, cached, err := p.schema(ctx, project, false)
	if err != nil {
		return nil, err
	}
	out, err := buildFields(schema, values)
	if err == nil || !cached {
		return out, err
	}
	if schema, _, err = p.schema(ctx, project, true); err != nil {
		return nil, err
	}
	return buildFields(schema, values)
}

func (p *Provider) schema(ctx context.Context, project apiProject, fresh bool) (map[string]projectField, bool, error) {
	p.mu.Lock()
	c, ok := p.schemas[project.ID]
	p.mu.Unlock()
	if !fresh && ok && p.now().Sub(c.at) < schemaTTL {
		return c.fields, true, nil
	}
	fields, err := p.loadSchema(ctx, project)
	if err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	p.schemas[project.ID] = cachedSchema{fields: fields, at: p.now()}
	p.mu.Unlock()
	return fields, false, nil
}

// loadSchema reads a project's fields. The settings endpoint answers an empty list or 403 to users without admin rights on the
// project, so the schema then comes from the projectCustomField of recent issues; several, because conditional fields only show
// on the issues they apply to.
func (p *Provider) loadSchema(ctx context.Context, project apiProject) (map[string]projectField, error) {
	var fields []projectField
	q := url.Values{"fields": {projectFieldList}, "$top": {"-1"}}
	if err := p.c.Get(ctx, "admin/projects/"+url.PathEscape(project.ID)+"/customFields", q, &fields); err != nil && !isForbidden(err) {
		return nil, classify(err)
	}
	out := make(map[string]projectField, len(fields))
	for _, f := range fields {
		out[f.Field.Name] = f
	}
	if len(out) > 0 {
		return out, nil
	}
	var issues []struct {
		CustomFields []struct {
			ProjectCustomField *projectField `json:"projectCustomField"`
		} `json:"customFields"`
	}
	q = url.Values{"query": {"project: {" + project.ShortName + "} sort by: updated desc"}, "fields": {"customFields(projectCustomField(" + projectFieldList + "))"}, "$top": {"20"}}
	if err := p.c.Get(ctx, "issues", q, &issues); err != nil {
		return nil, classify(err)
	}
	for _, i := range issues {
		for _, cf := range i.CustomFields {
			if f := cf.ProjectCustomField; f != nil && f.Field.Name != "" {
				if _, seen := out[f.Field.Name]; !seen {
					out[f.Field.Name] = *f
				}
			}
		}
	}
	if len(out) == 0 && len(issues) == 0 {
		return nil, fmt.Errorf("can't read the fields of %s: it has no issues, and this account can't read project settings", project.ShortName)
	}
	return out, nil
}

// ---- knowledge base ----

type apiArticle struct {
	IDReadable    string     `json:"idReadable"`
	Summary       string     `json:"summary"`
	Content       string     `json:"content"`
	Updated       int64      `json:"updated"`
	Project       apiProject `json:"project"`
	ParentArticle *struct {
		IDReadable string `json:"idReadable"`
	} `json:"parentArticle"`
	ChildArticles []struct {
		IDReadable string `json:"idReadable"`
	} `json:"childArticles"`
}

func (p *Provider) article(a apiArticle) tracker.Article {
	out := tracker.Article{ID: a.IDReadable, Title: a.Summary, URL: p.c.BaseURL() + "/articles/" + a.IDReadable, Content: a.Content, Project: a.Project.ShortName, UpdatedAt: isoMillis(a.Updated), HasChildren: len(a.ChildArticles) > 0}
	if a.ParentArticle != nil {
		out.ParentID = a.ParentArticle.IDReadable
	}
	return out
}

// SearchArticles implements tracker.KnowledgeProvider. The public docs list only fields, $skip and $top for /api/articles, though
// instances take query. So what comes back is also filtered here by the words of the query: an instance ignoring the parameter
// gives fewer hits rather than wrong ones.
func (p *Provider) SearchArticles(ctx context.Context, query, base string, limit int) ([]tracker.Article, error) {
	q := strings.TrimSpace(strings.TrimSpace(base) + " " + strings.TrimSpace(query))
	var raw []apiArticle
	if err := p.c.Get(ctx, "articles", url.Values{"query": {q}, "fields": {articleFields}, "$top": {strconv.Itoa(max(limit*3, 20))}}, &raw); err != nil {
		return nil, classify(err)
	}
	terms := tracker.FreeTextTerms(query)
	out := make([]tracker.Article, 0, limit)
	for _, a := range raw {
		hay := strings.ToLower(a.Summary + "\n" + a.Content)
		match := len(terms) == 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				match = true
				break
			}
		}
		if match {
			out = append(out, p.article(a))
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ListArticles implements tracker.KnowledgeProvider.
func (p *Provider) ListArticles(ctx context.Context, parentID, base string, page tracker.Page) ([]tracker.Article, bool, error) {
	q := url.Values{"fields": {articleFields}, "$skip": {strconv.Itoa(page.Offset)}, "$top": {strconv.Itoa(page.Limit + 1)}}
	path := "articles"
	if parentID != "" {
		path = "articles/" + url.PathEscape(parentID) + "/childArticles"
	} else if strings.TrimSpace(base) != "" {
		q.Set("query", strings.TrimSpace(base))
	}
	var raw []apiArticle
	if err := p.c.Get(ctx, path, q, &raw); err != nil {
		return nil, false, classify(err)
	}
	hasMore := len(raw) > page.Limit
	if hasMore {
		raw = raw[:page.Limit]
	}
	out := make([]tracker.Article, 0, len(raw))
	for _, a := range raw {
		if parentID == "" && a.ParentArticle != nil {
			continue
		}
		out = append(out, p.article(a))
	}
	return out, hasMore, nil
}

// GetArticle implements tracker.KnowledgeProvider.
func (p *Provider) GetArticle(ctx context.Context, id string) (*tracker.Article, error) {
	var raw apiArticle
	if err := p.c.Get(ctx, "articles/"+url.PathEscape(id), Fields(articleFields), &raw); err != nil {
		return nil, classify(err)
	}
	out := p.article(raw)
	return &out, nil
}
