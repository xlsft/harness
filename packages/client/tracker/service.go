package tracker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
)

// ServerAPI is what this package needs of the server (implemented by *syncclient.Client). Nil on a client with no server.
type ServerAPI interface {
	ClaimDeviceWork(ctx context.Context, req *libraryv1.ClaimDeviceWorkRequest) (*libraryv1.ClaimDeviceWorkResponse, error)
	ReportDeviceWork(ctx context.Context, req *libraryv1.ReportDeviceWorkRequest) (*libraryv1.ReportDeviceWorkResponse, error)
	ForgetDeviceConnection(ctx context.Context, req *libraryv1.ForgetDeviceConnectionRequest) (*libraryv1.ForgetDeviceConnectionResponse, error)
	PrepareCardSession(ctx context.Context, req *libraryv1.PrepareCardSessionRequest) (*libraryv1.PrepareCardSessionResponse, error)
	SearchKnowledge(ctx context.Context, req *libraryv1.SearchKnowledgeRequest) (*libraryv1.SearchKnowledgeResponse, error)
	ListKnowledgeSources(ctx context.Context, req *libraryv1.ListKnowledgeSourcesRequest) (*libraryv1.ListKnowledgeSourcesResponse, error)
	BrowseKnowledgeSource(ctx context.Context, req *libraryv1.BrowseKnowledgeSourceRequest) (*libraryv1.BrowseKnowledgeSourceResponse, error)
	GetKnowledgeDocument(ctx context.Context, req *libraryv1.GetKnowledgeDocumentRequest) (*libraryv1.GetKnowledgeDocumentResponse, error)
}

// Service is this device's tracker side: the store, the folder links, the worker and Knowledge's merged view.
type Service struct {
	Store *Store
	// Knowledge is this device's knowledge source connections, which may borrow Store's sign-ins.
	Knowledge *KnowledgeStore
	Folder    *FolderLinks
	Server    ServerAPI

	flowsMu sync.Mutex
	flows   map[string]*oauthFlow
}

// New is the service under home. Tracker connections that still served Knowledge themselves become knowledge source connections
// borrowing them here (MigrateKnowledge); a failure leaves them as they are, to try again at the next start.
func New(home string, server ServerAPI) *Service {
	store := NewStore(home)
	s := &Service{Store: store, Knowledge: NewKnowledgeStore(home, store), Folder: &FolderLinks{Path: filepath.Join(home, "tracker-folder-projects.json")}, Server: server, flows: map[string]*oauthFlow{}}
	_, _ = s.Knowledge.MigrateKnowledge()
	return s
}

// CapabilitiesProto is every provider's capabilities.
func CapabilitiesProto() []*libraryv1.TrackerCapabilities {
	var out []*libraryv1.TrackerCapabilities
	for _, f := range Factories() {
		out = append(out, capabilitiesProto(f.Capabilities))
	}
	return out
}

func capabilitiesProto(c Capabilities) *libraryv1.TrackerCapabilities {
	return &libraryv1.TrackerCapabilities{
		Provider: c.Provider, DisplayName: c.DisplayName, AuthMethods: authMethodsProto(c.AuthMethods), QueryLanguage: c.QueryLanguage, QueryExample: c.QueryExample,
		CustomFields: c.CustomFields, Comments: c.Comments, OptionFields: optionFieldsProto(c.OptionFields), DocsUrl: c.DocsURL, Logo: c.Logo,
		BaseUrlPlaceholder: c.BaseURLPlaceholder, DefaultBaseUrl: c.DefaultBaseURL, AuthHints: authHintsProto(c.AuthMethods, c.AuthHints),
	}
}

func authMethodsProto(list []AuthMethod) []libraryv1.TrackerAuthMethod {
	out := make([]libraryv1.TrackerAuthMethod, 0, len(list))
	for _, m := range list {
		out = append(out, AuthProto(m))
	}
	return out
}

func optionFieldsProto(list []OptionField) []*libraryv1.ProviderOptionField {
	out := make([]*libraryv1.ProviderOptionField, 0, len(list))
	for _, f := range list {
		out = append(out, &libraryv1.ProviderOptionField{Key: f.Key, Label: f.Label, Placeholder: f.Placeholder, Required: f.Required, AllowedValues: f.AllowedValues, Help: f.Help, DefaultValue: f.Default})
	}
	return out
}

// authHintsProto keeps the order of the methods, so the form lists them as the provider does.
func authHintsProto(methods []AuthMethod, hints map[AuthMethod]AuthHint) []*libraryv1.ProviderAuthHint {
	var out []*libraryv1.ProviderAuthHint
	for _, m := range methods {
		if h, ok := hints[m]; ok {
			out = append(out, &libraryv1.ProviderAuthHint{Method: AuthProto(m), Help: h.Help, SecretLabel: h.SecretLabel, LoginLabel: h.LoginLabel})
		}
	}
	return out
}

// AuthProto is the method's enum value.
func AuthProto(m AuthMethod) libraryv1.TrackerAuthMethod {
	switch m {
	case AuthToken:
		return libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_TOKEN
	case AuthPassword:
		return libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_PASSWORD
	case AuthOAuth:
		return libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_OAUTH
	case AuthEmailToken:
		return libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_EMAIL_TOKEN
	case AuthVCS:
		return libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_VCS_CONNECTION
	}
	return libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_UNSPECIFIED
}

// AuthFromProto is the enum value's method.
func AuthFromProto(m libraryv1.TrackerAuthMethod) AuthMethod {
	switch m {
	case libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_TOKEN:
		return AuthToken
	case libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_PASSWORD:
		return AuthPassword
	case libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_OAUTH:
		return AuthOAuth
	case libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_EMAIL_TOKEN:
		return AuthEmailToken
	case libraryv1.TrackerAuthMethod_TRACKER_AUTH_METHOD_VCS_CONNECTION:
		return AuthVCS
	}
	return ""
}

// ConnectionProto shows a connection; the secret stays out.
func (s *Service) ConnectionProto(c Connection) *libraryv1.TrackerConnection {
	p := &libraryv1.TrackerConnection{
		Id: c.ID, Scope: libraryv1.TrackerConnectionScope_TRACKER_CONNECTION_SCOPE_DEVICE, Provider: c.Provider, Name: c.Name, BaseUrl: c.BaseURL, Auth: AuthProto(c.Auth), Login: c.Login, OauthClientId: c.OAuthClientID,
		HasSecret: s.Store.HasSecret(c), Enabled: c.Enabled, Options: c.Options, Healthy: c.Healthy, LastError: c.LastError, LastCheckedAt: c.LastCheckedAt,
	}
	if f, err := factoryOf(c.Provider); err == nil {
		p.Capabilities = capabilitiesProto(f.Capabilities)
	}
	return p
}

// InputFromProto reads a form.
func InputFromProto(c *libraryv1.TrackerConnectionInput) Input {
	return Input{
		Connection: Connection{ID: c.GetId(), Provider: c.GetProvider(), Name: c.GetName(), BaseURL: c.GetBaseUrl(), Auth: AuthFromProto(c.GetAuth()), Login: strings.TrimSpace(c.GetLogin()), OAuthClientID: strings.TrimSpace(c.GetOauthClientId()), Enabled: c.GetEnabled(), Options: c.GetOptions()},
		Secret:     c.Secret, OAuthClientSecret: c.OauthClientSecret,
	}
}

// IssueProto shows an issue.
func IssueProto(i *Issue) *libraryv1.TrackerIssue {
	p := &libraryv1.TrackerIssue{Id: i.ID, Title: i.Title, Url: i.URL, Description: i.Description, Project: i.Project, UpdatedAt: i.UpdatedAt}
	for _, f := range i.Fields {
		p.Fields = append(p.Fields, &libraryv1.TrackerIssueField{Name: f.Name, Value: f.Value})
	}
	for _, c := range i.Comments {
		p.Comments = append(p.Comments, &libraryv1.TrackerIssueComment{Author: c.Author, Text: c.Text, CreatedAt: c.CreatedAt})
	}
	return p
}

// ProjectsProto lists a device connection's projects.
func (s *Service) ProjectsProto(ctx context.Context, connectionID string) ([]*libraryv1.ProviderProject, error) {
	_, p, err := s.Store.OpenByID(connectionID)
	if err != nil {
		return nil, err
	}
	list, err := p.Projects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*libraryv1.ProviderProject, 0, len(list))
	for _, x := range list {
		out = append(out, &libraryv1.ProviderProject{Key: x.Key, Metadata: &libraryv1.ProjectMetadata{Ticker: x.Key, Name: x.Name, Description: x.Description, Logo: x.Logo}})
	}
	return out, nil
}

// GetIssue reads an issue through a connection of this device.
func (s *Service) GetIssue(ctx context.Context, connectionID, id string) (*libraryv1.TrackerIssue, error) {
	_, p, err := s.Store.OpenByID(connectionID)
	if err != nil {
		return nil, err
	}
	i, err := p.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return IssueProto(i), nil
}

// Comment posts on an issue through a connection of this device.
func (s *Service) Comment(ctx context.Context, connectionID, id, text string) error {
	_, p, err := s.Store.OpenByID(connectionID)
	if err != nil {
		return err
	}
	return p.Comment(ctx, id, text)
}

// Forget deletes a connection, here and in the stages using it. The server is told first: a connection left behind on a stage
// would wait for a device that no longer has it, while a failure here can be repeated.
func (s *Service) Forget(ctx context.Context, id string) error {
	if s.Server != nil {
		if _, err := s.Server.ForgetDeviceConnection(ctx, &libraryv1.ForgetDeviceConnectionRequest{ConnectionId: id}); err != nil {
			return err
		}
	}
	return s.Store.Delete(id)
}

// ---- folder links ----

// FolderLinks says which project a folder belongs to on this device. Paths belong to a machine, so it is never synced.
type FolderLinks struct {
	Path string
	mu   sync.Mutex
}

type folderFile struct {
	Links map[string]string `json:"links"`
}

func (f *FolderLinks) load() (map[string]string, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out folderFile
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Links == nil {
		out.Links = map[string]string{}
	}
	return out.Links, nil
}

func canonical(folder string) string {
	p, err := filepath.Abs(folder)
	if err != nil {
		p = filepath.Clean(folder)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// List gives folder -> project id.
func (f *FolderLinks) List() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.load()
}

// Set links a folder, or unlinks it with an empty project id.
func (f *FolderLinks) Set(folder, projectID string) error {
	if strings.TrimSpace(folder) == "" {
		return errors.New("a folder is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	links, err := f.load()
	if err != nil {
		return err
	}
	key := canonical(folder)
	if projectID == "" {
		delete(links, key)
	} else {
		links[key] = projectID
	}
	return writeJSON(f.Path, folderFile{links})
}

// ProjectOf is the project of a folder or of the nearest folder above it that has one, as a worktree or subfolder of a linked
// repository belongs to it. "" when none has.
func (f *FolderLinks) ProjectOf(folder string) string {
	if folder == "" {
		return ""
	}
	links, err := f.List()
	if err != nil || len(links) == 0 {
		return ""
	}
	dir := canonical(folder)
	for {
		if id, ok := links[dir]; ok {
			return id
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
}

// FolderOf is a folder linked to a project; the shortest path when several are, the repository rather than a part of it.
func (f *FolderLinks) FolderOf(projectID string) string {
	links, err := f.List()
	if err != nil {
		return ""
	}
	best := ""
	for dir, id := range links {
		if id == projectID && (best == "" || len(dir) < len(best) || (len(dir) == len(best) && dir < best)) {
			best = dir
		}
	}
	return best
}

// ---- a session from an issue ----

// PrepareSession is where a session started from an issue begins, on this device.
func (s *Service) PrepareSession(ctx context.Context, cardID string) (folder, branch, prompt string, err error) {
	if s.Server == nil {
		return "", "", "", errors.New("issues live on the server, and this client has none")
	}
	res, err := s.Server.PrepareCardSession(ctx, &libraryv1.PrepareCardSessionRequest{Id: cardID})
	if err != nil {
		return "", "", "", err
	}
	return s.Folder.FolderOf(res.GetProjectId()), res.GetBranch(), res.GetPrompt(), nil
}

// ---- the worker ----

const (
	workerTick = 30 * time.Second
	// inputIssues is how many issues one poll takes; the once-only ledger lets the next one go on from there.
	inputIssues = 200
	reportWait  = 2 * time.Minute
)

// Run does this device's board work until ctx ends.
func (s *Service) Run(ctx context.Context, logf func(format string, args ...any)) {
	if s.Server == nil {
		return
	}
	t := time.NewTicker(workerTick)
	defer t.Stop()
	for {
		if _, errs := s.SyncOnce(ctx); logf != nil {
			for _, e := range errs {
				logf("tracker sync: %s", e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncOnce claims what is due on this device's connections, does it and reports. Errors are what the server could not be told;
// a provider's failure is reported to the server, and shows on the stage or the issue.
func (s *Service) SyncOnce(ctx context.Context) (int, []string) {
	if s.Server == nil {
		return 0, nil
	}
	conns, err := s.Store.List()
	if err != nil {
		return 0, []string{err.Error()}
	}
	var ids []string
	for _, c := range conns {
		if c.Enabled {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, reportWait)
	defer cancel()
	work, err := s.Server.ClaimDeviceWork(ctx, &libraryv1.ClaimDeviceWorkRequest{ConnectionIds: ids})
	if err != nil {
		return 0, []string{err.Error()}
	}
	providers := map[string]Provider{}
	open := func(id string) (Provider, error) {
		if p, ok := providers[id]; ok {
			return p, nil
		}
		_, p, err := s.Store.OpenByID(id)
		if err != nil {
			return nil, err
		}
		providers[id] = p
		return p, nil
	}
	report := &libraryv1.ReportDeviceWorkRequest{}
	for _, in := range work.GetInputs() {
		r := &libraryv1.DeviceInputResult{InputId: in.GetInputId()}
		p, err := open(in.GetConnectionId())
		if err == nil {
			var issues []Issue
			issues, _, err = p.Search(ctx, in.GetQuery(), Page{Limit: inputIssues})
			for i := range issues {
				// Only what makes a card goes to the server: never a description or comments.
				r.Issues = append(r.Issues, &libraryv1.TrackerIssue{Id: issues[i].ID, Title: issues[i].Title, Url: issues[i].URL})
			}
		}
		if err != nil {
			r.Error = err.Error()
		}
		report.Inputs = append(report.Inputs, r)
	}
	for _, out := range work.GetOutputs() {
		r := &libraryv1.DeviceOutputResult{OutputId: out.GetOutputId()}
		p, err := open(out.GetConnectionId())
		if err == nil {
			err = applyOutput(ctx, p, out)
		}
		if err != nil {
			r.Error, r.Transient = err.Error(), IsTransient(err)
		}
		report.Outputs = append(report.Outputs, r)
	}
	for _, c := range work.GetChecks() {
		p, err := open(c.GetConnectionId())
		if err != nil {
			continue
		}
		issue, err := p.Get(ctx, c.GetExternalId())
		switch {
		case errors.Is(err, ErrNotFound):
			report.Checks = append(report.Checks, &libraryv1.DeviceCheckResult{CardId: c.GetCardId(), Missing: true})
		case err == nil:
			report.Checks = append(report.Checks, &libraryv1.DeviceCheckResult{CardId: c.GetCardId(), Title: issue.Title, Url: issue.URL})
		}
	}
	if len(report.Inputs)+len(report.Outputs)+len(report.Checks) == 0 {
		return 0, nil
	}
	res, err := s.Server.ReportDeviceWork(ctx, report)
	if err != nil {
		return 0, []string{err.Error()}
	}
	return int(res.GetNewCards()), nil
}

func applyOutput(ctx context.Context, p Provider, out *libraryv1.DeviceOutput) error {
	if out.GetExternalId() == "" {
		return errors.New("the card is no longer linked to an issue")
	}
	var fields []FieldValue
	for _, f := range out.GetFields() {
		fields = append(fields, FieldValue{Name: f.GetName(), Value: f.GetValue()})
	}
	if len(fields) > 0 {
		if err := p.Update(ctx, out.GetExternalId(), IssueUpdate{Fields: fields}); err != nil {
			return err
		}
	}
	if out.GetComment() != "" {
		return p.Comment(ctx, out.GetExternalId(), out.GetComment())
	}
	return nil
}

// ---- OAuth through a loopback address ----

const oauthFlowTTL = 10 * time.Minute

type oauthFlow struct {
	done  bool
	err   string
	close func()
}

// StartOAuth starts Authorization Code with PKCE. A listener on the loopback takes the redirect, as the docs allow for a registered
// loopback IP URI; the port is the one the listener got, so the client on the instance should accept any.
func (s *Service) StartOAuth(ctx context.Context, connectionID string) (flowID, authorizeURL string, err error) {
	c, err := s.Store.Get(connectionID)
	if err != nil {
		return "", "", err
	}
	if c.Auth != AuthOAuth {
		return "", "", errors.New("this connection does not use OAuth")
	}
	f, err := factoryOf(c.Provider)
	if err != nil {
		return "", "", err
	}
	if f.OAuth == nil {
		return "", "", fmt.Errorf("%s has no OAuth", f.Capabilities.DisplayName)
	}
	ex, err := f.OAuth(c.BaseURL)
	if err != nil {
		return "", "", err
	}
	creds, err := s.Store.credentials(c.ID)
	if err != nil {
		return "", "", err
	}
	return s.loopbackOAuth(ctx, ex, c.OAuthClientID, creds.ClientSecret, func(t Credentials) error { return s.Store.SetTokens(c.ID, t) })
}

// StartKnowledgeOAuth is StartOAuth for a knowledge source connection with its own OAuth sign-in. One that borrows is authorized
// through the tracker connection it borrows.
func (s *Service) StartKnowledgeOAuth(ctx context.Context, connectionID string) (flowID, authorizeURL string, err error) {
	c, err := s.Knowledge.Get(connectionID)
	if err != nil {
		return "", "", err
	}
	if c.Borrow != "" {
		return "", "", errors.New("this source uses the sign-in of a tracker connection: authorize that one")
	}
	if c.Auth != AuthOAuth {
		return "", "", errors.New("this connection does not use OAuth")
	}
	f, err := knowledgeFactoryOf(c.Provider)
	if err != nil {
		return "", "", err
	}
	if f.OAuth == nil {
		return "", "", fmt.Errorf("%s has no OAuth", f.Capabilities.DisplayName)
	}
	ex, err := f.OAuth(c.BaseURL)
	if err != nil {
		return "", "", err
	}
	creds, err := readCredentials(s.Knowledge.Secrets, c.ID)
	if err != nil {
		return "", "", err
	}
	return s.loopbackOAuth(ctx, ex, c.OAuthClientID, creds.ClientSecret, func(t Credentials) error { return s.Knowledge.SetTokens(c.ID, t) })
}

func (s *Service) loopbackOAuth(ctx context.Context, ex OAuthExchange, clientID, clientSecret string, store func(Credentials) error) (flowID, authorizeURL string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	state, verifier := randomString(16), randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	authURL, err := ex.AuthorizeURL(ctx, clientID, redirect, state, base64.RawURLEncoding.EncodeToString(sum[:]))
	if err != nil {
		_ = ln.Close()
		return "", "", err
	}
	flowID = randomString(12)
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	// Shutdown, not Close: the callback's own response must reach the browser before the listener goes.
	flow := &oauthFlow{close: func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(ctx) != nil {
			_ = srv.Close()
		}
	}}
	s.flowsMu.Lock()
	if s.flows == nil {
		s.flows = map[string]*oauthFlow{}
	}
	s.flows[flowID] = flow
	s.flowsMu.Unlock()
	finish := func(msg string) {
		s.flowsMu.Lock()
		flow.done, flow.err = true, msg
		s.flowsMu.Unlock()
		go flow.close()
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "This authorization link is not valid.", http.StatusBadRequest)
			return
		}
		msg := ""
		if q.Get("error") != "" {
			msg = "The instance refused the authorization."
		} else {
			ectx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			t, err := ex.Exchange(ectx, clientID, clientSecret, q.Get("code"), redirect, verifier)
			if err == nil {
				err = store(t)
			}
			if err != nil {
				msg = err.Error()
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if msg != "" {
			fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Authorization failed</title><p>%s</p>", html.EscapeString(msg))
		} else {
			fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>Connected</title><p>The connection is authorized. You can close this window.</p>")
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		finish(msg)
	})
	go func() { _ = srv.Serve(ln) }()
	time.AfterFunc(oauthFlowTTL, func() {
		s.flowsMu.Lock()
		over := !flow.done
		s.flowsMu.Unlock()
		if over {
			finish("This authorization took too long. Start it again.")
		}
	})
	return flowID, authURL, nil
}

// OAuthStatus tells how a flow went.
func (s *Service) OAuthStatus(flowID string) (done bool, errText string, err error) {
	s.flowsMu.Lock()
	defer s.flowsMu.Unlock()
	f, ok := s.flows[flowID]
	if !ok {
		return false, "", fmt.Errorf("%w: flow", ErrNotFound)
	}
	return f.done, f.err, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
