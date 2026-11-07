package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	libraryv1 "github.com/xlsft/harness/packages/client/gen/harness/library/v1"
)

// fakeProvider registers as a provider in tests.
type fakeProvider struct {
	mu       sync.Mutex
	issues   map[string]*Issue
	failWith error
	updates  []string
	comments []string
	articles []Article
	oauth    *fakeOAuth
}

var (
	current   *fakeProvider
	currentMu sync.Mutex
)

func init() {
	Register("fake", Factory{
		Capabilities: Capabilities{Provider: "fake", DisplayName: "Fake", AuthMethods: []AuthMethod{AuthToken, AuthOAuth}, Knowledge: true},
		Open: func(o OpenOptions) (Provider, error) {
			currentMu.Lock()
			defer currentMu.Unlock()
			if o.Auth == AuthToken && o.Credentials.Secret == "" {
				return nil, fmt.Errorf("%w: no token", ErrAuth)
			}
			return current, nil
		},
		OAuth: func(string) (OAuthExchange, error) { return current.oauth, nil },
	})
}

func use(p *fakeProvider) {
	currentMu.Lock()
	defer currentMu.Unlock()
	current = p
}

func (*fakeProvider) Capabilities() Capabilities {
	return Capabilities{Provider: "fake", Knowledge: true}
}

func (p *fakeProvider) Test(context.Context) (Identity, error) {
	return Identity{Login: "alice"}, p.failWith
}

func (p *fakeProvider) Search(_ context.Context, _ string, _ Page) ([]Issue, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil {
		return nil, false, p.failWith
	}
	var out []Issue
	for _, i := range p.issues {
		out = append(out, *i)
	}
	return out, false, nil
}

func (p *fakeProvider) Get(_ context.Context, id string) (*Issue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil {
		return nil, p.failWith
	}
	if i, ok := p.issues[id]; ok {
		return i, nil
	}
	return nil, ErrNotFound
}

func (*fakeProvider) Create(context.Context, IssueDraft) (*Issue, error) { return nil, nil }

func (p *fakeProvider) Update(_ context.Context, id string, u IssueUpdate) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil {
		return p.failWith
	}
	p.updates = append(p.updates, id+":"+u.Fields[0].Name+"="+u.Fields[0].Value)
	return nil
}

func (p *fakeProvider) Comment(_ context.Context, _, text string) error {
	p.comments = append(p.comments, text)
	return nil
}

func (*fakeProvider) Fields(context.Context, string) ([]Field, error) { return nil, nil }
func (*fakeProvider) Projects(context.Context) ([]Project, error) {
	return []Project{{Key: "DEMO", Name: "Demo"}}, nil
}

func (p *fakeProvider) SearchArticles(_ context.Context, query, _ string, _ int) ([]Article, error) {
	if p.failWith != nil {
		return nil, p.failWith
	}
	var out []Article
	for _, a := range p.articles {
		if strings.Contains(strings.ToLower(a.Content), strings.ToLower(query)) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (p *fakeProvider) ListArticles(context.Context, string, string, Page) ([]Article, bool, error) {
	return p.articles, false, nil
}

func (p *fakeProvider) GetArticle(_ context.Context, id string) (*Article, error) {
	for i := range p.articles {
		if p.articles[i].ID == id {
			return &p.articles[i], nil
		}
	}
	return nil, ErrNotFound
}

// fakeServer is the server's side of the worker and of Knowledge.
type fakeServer struct {
	mu        sync.Mutex
	claim     *libraryv1.ClaimDeviceWorkResponse
	claimed   [][]string
	report    *libraryv1.ReportDeviceWorkRequest
	forgot    []string
	forgetErr error
	searchErr error
	hits      []*libraryv1.KnowledgeHit
	project   string
}

func (f *fakeServer) ClaimDeviceWork(_ context.Context, req *libraryv1.ClaimDeviceWorkRequest) (*libraryv1.ClaimDeviceWorkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimed = append(f.claimed, req.GetConnectionIds())
	return f.claim, nil
}

func (f *fakeServer) ReportDeviceWork(_ context.Context, req *libraryv1.ReportDeviceWorkRequest) (*libraryv1.ReportDeviceWorkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.report = req
	return &libraryv1.ReportDeviceWorkResponse{NewCards: int32(len(req.GetInputs()))}, nil
}

func (f *fakeServer) ForgetDeviceConnection(_ context.Context, req *libraryv1.ForgetDeviceConnectionRequest) (*libraryv1.ForgetDeviceConnectionResponse, error) {
	if f.forgetErr != nil {
		return nil, f.forgetErr
	}
	f.forgot = append(f.forgot, req.GetConnectionId())
	return &libraryv1.ForgetDeviceConnectionResponse{}, nil
}

func (f *fakeServer) PrepareCardSession(context.Context, *libraryv1.PrepareCardSessionRequest) (*libraryv1.PrepareCardSessionResponse, error) {
	return &libraryv1.PrepareCardSessionResponse{Branch: "issue/x", Prompt: "Work", ProjectId: f.project}, nil
}

func (f *fakeServer) SearchKnowledge(context.Context, *libraryv1.SearchKnowledgeRequest) (*libraryv1.SearchKnowledgeResponse, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return &libraryv1.SearchKnowledgeResponse{Hits: f.hits}, nil
}

func (f *fakeServer) ListKnowledgeSources(context.Context, *libraryv1.ListKnowledgeSourcesRequest) (*libraryv1.ListKnowledgeSourcesResponse, error) {
	return &libraryv1.ListKnowledgeSourcesResponse{Sources: []*libraryv1.KnowledgeSource{{Ref: &libraryv1.KnowledgeSourceRef{Id: "internal:org"}}}}, nil
}

func (f *fakeServer) BrowseKnowledgeSource(_ context.Context, req *libraryv1.BrowseKnowledgeSourceRequest) (*libraryv1.BrowseKnowledgeSourceResponse, error) {
	return &libraryv1.BrowseKnowledgeSourceResponse{Documents: []*libraryv1.KnowledgeDocument{{Id: "kb_1", Title: req.GetSourceId()}}}, nil
}

func (f *fakeServer) GetKnowledgeDocument(_ context.Context, req *libraryv1.GetKnowledgeDocumentRequest) (*libraryv1.GetKnowledgeDocumentResponse, error) {
	return &libraryv1.GetKnowledgeDocumentResponse{Document: &libraryv1.KnowledgeDocument{Id: req.GetId()}, Content: "from the server"}, nil
}

func testService(t *testing.T, server ServerAPI) *Service {
	t.Helper()
	t.Setenv("HARNESS_KEYRING", "0")
	return New(t.TempDir(), server)
}

func strPtr(s string) *string { return &s }

func saved(t *testing.T, s *Service, c Connection, secret string) Connection {
	t.Helper()
	c.Provider = "fake"
	if c.BaseURL == "" {
		c.BaseURL = "yt.example.test"
	}
	if c.Auth == "" {
		c.Auth = AuthToken
	}
	c.Enabled = true
	out, err := s.Store.Save(Input{Connection: c, Secret: strPtr(secret)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSecretsStayOutOfTheConnectionFile(t *testing.T) {
	s := testService(t, nil)
	use(&fakeProvider{})
	c := saved(t, s, Connection{Name: "Mine"}, "perm:supersecret")
	if !strings.HasPrefix(c.ID, "tc_") || c.BaseURL != "https://yt.example.test" {
		t.Fatalf("%+v", c)
	}
	data, err := os.ReadFile(s.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "supersecret") {
		t.Fatal("the secret is in the plain config")
	}
	p := s.ConnectionProto(c)
	if !p.GetHasSecret() || strings.Contains(p.String(), "supersecret") {
		t.Fatalf("%v", p)
	}
	info, err := os.Stat(filepath.Dir(s.Store.Path) + "/tracker-connection-keys.json")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the fallback file must be 0600: %v %v", info, err)
	}
	// An unset secret keeps the stored one, a changed instance forgets tokens.
	c2, err := s.Store.Save(Input{Connection: c})
	if err != nil || !s.Store.HasSecret(c2) {
		t.Fatalf("%v", err)
	}
	if _, err := s.Store.Save(Input{Connection: Connection{Provider: "fake", BaseURL: "ftp://x", Auth: AuthToken}}); err == nil {
		t.Fatal("a non-http address must be refused")
	}
	if _, err := s.Store.Save(Input{Connection: Connection{Provider: "nope", BaseURL: "x.test", Auth: AuthToken}}); err == nil {
		t.Fatal("an unknown provider must be refused")
	}
	if _, err := s.Store.Save(Input{Connection: Connection{Provider: "fake", BaseURL: "x.test", Auth: AuthOAuth}}); err == nil {
		t.Fatal("OAuth needs a client id")
	}
}

func TestForgetTellsTheServerFirst(t *testing.T) {
	srv := &fakeServer{forgetErr: errors.New("down")}
	s := testService(t, srv)
	use(&fakeProvider{})
	c := saved(t, s, Connection{Name: "Mine"}, "t")
	if err := s.Forget(context.Background(), c.ID); err == nil {
		t.Fatal("a failure must show")
	}
	if _, err := s.Store.Get(c.ID); err != nil {
		t.Fatalf("the connection must stay for another try: %v", err)
	}
	srv.forgetErr = nil
	if err := s.Forget(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Get(c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if v, _ := s.Store.Secrets.Get(c.ID); v != "" {
		t.Fatal("the secret must go with it")
	}
	if len(srv.forgot) != 1 {
		t.Fatalf("%v", srv.forgot)
	}
}

func TestWorkerDoesTheBoardsWorkFromHere(t *testing.T) {
	fp := &fakeProvider{issues: map[string]*Issue{"V-1": {ID: "V-1", Title: "From VPN", URL: "https://vpn/V-1", Description: "SECRET BODY"}}}
	use(fp)
	srv := &fakeServer{}
	s := testService(t, srv)
	c := saved(t, s, Connection{Name: "Mine"}, "t")
	off := saved(t, s, Connection{Name: "Off"}, "t")
	off.Enabled = false
	if _, err := s.Store.Save(Input{Connection: off}); err != nil {
		t.Fatal(err)
	}
	srv.claim = &libraryv1.ClaimDeviceWorkResponse{
		Inputs: []*libraryv1.DeviceInput{{InputId: "in1", ConnectionId: c.ID, Query: "q"}},
		Outputs: []*libraryv1.DeviceOutput{
			{OutputId: "ok", ConnectionId: c.ID, ExternalId: "V-1", Fields: []*libraryv1.StageOutputField{{Name: "Stage", Value: "Review"}}, Comment: "Done"},
			{OutputId: "gone", ConnectionId: c.ID, Fields: []*libraryv1.StageOutputField{{Name: "Stage", Value: "Review"}}},
		},
		Checks: []*libraryv1.DeviceCheck{{CardId: "c1", ConnectionId: c.ID, ExternalId: "V-1"}, {CardId: "c2", ConnectionId: c.ID, ExternalId: "V-2"}},
	}
	n, errs := s.SyncOnce(context.Background())
	if n != 1 || len(errs) != 0 {
		t.Fatalf("%d %v", n, errs)
	}
	if strings.Join(srv.claimed[0], ",") != c.ID {
		t.Fatalf("only enabled connections are claimed for: %v", srv.claimed)
	}
	r := srv.report
	if len(r.GetInputs()[0].GetIssues()) != 1 || r.GetInputs()[0].GetIssues()[0].GetTitle() != "From VPN" {
		t.Fatalf("%v", r.GetInputs())
	}
	if strings.Contains(r.String(), "SECRET BODY") {
		t.Fatal("an issue's body must not go to the server")
	}
	if r.GetOutputs()[0].GetError() != "" || fp.updates[0] != "V-1:Stage=Review" || fp.comments[0] != "Done" {
		t.Fatalf("%v %v %v", r.GetOutputs(), fp.updates, fp.comments)
	}
	if !strings.Contains(r.GetOutputs()[1].GetError(), "no longer linked") {
		t.Fatalf("%v", r.GetOutputs()[1])
	}
	if r.GetChecks()[0].GetMissing() || r.GetChecks()[0].GetTitle() != "From VPN" || !r.GetChecks()[1].GetMissing() {
		t.Fatalf("%v", r.GetChecks())
	}
}

func TestWorkerReportsFailureKinds(t *testing.T) {
	fp := &fakeProvider{failWith: fmt.Errorf("%w: down", ErrTransient), issues: map[string]*Issue{}}
	use(fp)
	srv := &fakeServer{}
	s := testService(t, srv)
	c := saved(t, s, Connection{Name: "Mine"}, "t")
	srv.claim = &libraryv1.ClaimDeviceWorkResponse{
		Inputs:  []*libraryv1.DeviceInput{{InputId: "in1", ConnectionId: c.ID, Query: "q"}},
		Outputs: []*libraryv1.DeviceOutput{{OutputId: "o1", ConnectionId: c.ID, ExternalId: "V-1", Fields: []*libraryv1.StageOutputField{{Name: "S", Value: "R"}}}},
		Checks:  []*libraryv1.DeviceCheck{{CardId: "c1", ConnectionId: c.ID, ExternalId: "V-1"}},
	}
	s.SyncOnce(context.Background())
	r := srv.report
	if !strings.Contains(r.GetInputs()[0].GetError(), "down") {
		t.Fatalf("%v", r.GetInputs())
	}
	if !r.GetOutputs()[0].GetTransient() {
		t.Fatalf("%v", r.GetOutputs())
	}
	if len(r.GetChecks()) != 0 {
		t.Fatalf("a failing connection says nothing about the issue: %v", r.GetChecks())
	}
	fp.failWith = errors.New("not allowed")
	s.SyncOnce(context.Background())
	if srv.report.GetOutputs()[0].GetTransient() {
		t.Fatalf("%v", srv.report.GetOutputs())
	}
}

func TestWorkerWithoutConnectionsAsksNothing(t *testing.T) {
	srv := &fakeServer{}
	s := testService(t, srv)
	if n, errs := s.SyncOnce(context.Background()); n != 0 || errs != nil || len(srv.claimed) != 0 {
		t.Fatalf("%d %v %v", n, errs, srv.claimed)
	}
}

func TestFolderLinks(t *testing.T) {
	s := testService(t, nil)
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := s.Folder
	if f.ProjectOf(root) != "" {
		t.Fatal("no link yet")
	}
	if err := f.Set(root, "prj_1"); err != nil {
		t.Fatal(err)
	}
	if f.ProjectOf(sub) != "prj_1" || f.ProjectOf(root) != "prj_1" {
		t.Fatalf("%q", f.ProjectOf(sub))
	}
	if f.ProjectOf(filepath.Dir(root)) != "" {
		t.Fatal("a folder above the link is not in the project")
	}
	if err := f.Set(sub, "prj_2"); err != nil {
		t.Fatal(err)
	}
	if f.ProjectOf(sub) != "prj_2" {
		t.Fatal("the nearest link wins")
	}
	if want, _ := filepath.EvalSymlinks(root); f.FolderOf("prj_1") != want {
		t.Fatalf("%q", f.FolderOf("prj_1"))
	}
	if err := f.Set(sub, ""); err != nil {
		t.Fatal(err)
	}
	if f.ProjectOf(sub) != "prj_1" {
		t.Fatal("unlinking falls back")
	}
}

func TestPrepareSessionUsesTheLinkedFolder(t *testing.T) {
	srv := &fakeServer{project: "prj_1"}
	s := testService(t, srv)
	dir := t.TempDir()
	if err := s.Folder.Set(dir, "prj_1"); err != nil {
		t.Fatal(err)
	}
	folder, branch, prompt, err := s.PrepareSession(context.Background(), "ABC-1")
	if want, _ := filepath.EvalSymlinks(dir); err != nil || folder != want || branch != "issue/x" || prompt != "Work" {
		t.Fatalf("%q %q %q %v", folder, branch, prompt, err)
	}
	srv.project = "prj_other"
	if folder, _, _, _ := s.PrepareSession(context.Background(), "ABC-1"); folder != "" {
		t.Fatalf("%q", folder)
	}
}

func TestKnowledgeMergesSourcesAndSurvivesFailures(t *testing.T) {
	fp := &fakeProvider{articles: []Article{{ID: "A-1", Title: "Deploy", Content: "canary rollout\nother", URL: "https://x/A-1"}}}
	use(fp)
	srv := &fakeServer{hits: []*libraryv1.KnowledgeHit{
		{DocumentId: "kb_1", Title: "Internal", Source: &libraryv1.KnowledgeSourceRef{Id: "internal:org"}},
		{DocumentId: "kb_2", Title: "Second", Source: &libraryv1.KnowledgeSourceRef{Id: "internal:org"}},
	}}
	s := testService(t, srv)
	c := saved(t, s, Connection{Name: "Mine", KnowledgeEnabled: true}, "t")
	saved(t, s, Connection{Name: "NoDocs"}, "t")
	ctx := context.Background()

	hits, warnings, err := s.Search(ctx, "canary", 8, commonv1.Scope_SCOPE_UNSPECIFIED, nil)
	if err != nil || len(warnings) != 0 || len(hits) != 3 {
		t.Fatalf("%v %v %v", hits, warnings, err)
	}
	var local *libraryv1.KnowledgeHit
	for _, h := range hits {
		if h.GetSource().GetId() == c.ID {
			local = h
		}
	}
	if local == nil || local.GetDocumentId() != "conn:"+c.ID+":A-1" || local.GetSource().GetScope() != commonv1.Scope_SCOPE_USER || !strings.Contains(local.GetSnippet(), "canary") {
		t.Fatalf("%v", local)
	}
	// Global only: this device's sources are personal.
	hits, _, _ = s.Search(ctx, "canary", 8, commonv1.Scope_SCOPE_ORG, nil)
	if len(hits) != 2 {
		t.Fatalf("%v", hits)
	}
	// One source picked.
	hits, _, _ = s.Search(ctx, "canary", 8, commonv1.Scope_SCOPE_UNSPECIFIED, []string{c.ID})
	if len(hits) != 1 {
		t.Fatalf("%v", hits)
	}

	fp.failWith = errors.New("vpn is down")
	hits, warnings, err = s.Search(ctx, "canary", 8, commonv1.Scope_SCOPE_UNSPECIFIED, nil)
	if err != nil || len(hits) != 2 || len(warnings) != 1 || warnings[0].GetSource().GetId() != c.ID {
		t.Fatalf("%v %v %v", hits, warnings, err)
	}
	fp.failWith = nil
	srv.searchErr = errors.New("server down")
	hits, warnings, err = s.Search(ctx, "canary", 8, commonv1.Scope_SCOPE_UNSPECIFIED, nil)
	if err != nil || len(hits) != 1 || len(warnings) != 1 {
		t.Fatalf("the device's sources still answer: %v %v %v", hits, warnings, err)
	}

	// Reads route by the source.
	doc, content, err := s.Read(ctx, "conn:"+c.ID+":A-1")
	if err != nil || content != "canary rollout\nother" || doc.GetSource().GetId() != c.ID {
		t.Fatalf("%v %q %v", doc, content, err)
	}
	if _, content, err := s.Read(ctx, "kb_9"); err != nil || content != "from the server" {
		t.Fatalf("%q %v", content, err)
	}
	if _, content, err := s.Read(ctx, "conn:tc_company:A-1"); err != nil || content != "from the server" {
		t.Fatalf("an admin connection's document is the server's: %q %v", content, err)
	}
	docs, _, err := s.Browse(ctx, c.ID, "", 0, 0)
	if err != nil || len(docs) != 1 {
		t.Fatalf("%v %v", docs, err)
	}
	if docs, _, err := s.Browse(ctx, "internal:org", "", 0, 0); err != nil || docs[0].GetTitle() != "internal:org" {
		t.Fatalf("%v %v", docs, err)
	}
	sources, err := s.Sources(ctx)
	if err != nil || len(sources) != 2 {
		t.Fatalf("%v %v", sources, err)
	}
}

type fakeOAuth struct {
	wantVerifier string
	gotChallenge string
	gotRedirect  string
	exchangeErr  error
}

func (f *fakeOAuth) AuthorizeURL(_ context.Context, clientID, redirect, state, challenge string) (string, error) {
	f.gotChallenge, f.gotRedirect = challenge, redirect
	return "https://hub.test/auth?client_id=" + clientID + "&state=" + state, nil
}

func (f *fakeOAuth) Exchange(_ context.Context, _, _, code, redirect, verifier string) (Credentials, error) {
	f.wantVerifier = verifier
	if f.exchangeErr != nil {
		return Credentials{}, f.exchangeErr
	}
	if code != "good" || redirect != f.gotRedirect {
		return Credentials{}, errors.New("bad code")
	}
	return Credentials{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}

func TestOAuthThroughTheLoopback(t *testing.T) {
	fo := &fakeOAuth{}
	use(&fakeProvider{oauth: fo})
	s := testService(t, nil)
	c, err := s.Store.Save(Input{Connection: Connection{Provider: "fake", BaseURL: "x.test", Auth: AuthOAuth, OAuthClientID: "cli", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	flow, authURL, err := s.StartOAuth(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fo.gotRedirect, "http://127.0.0.1:") || fo.gotChallenge == "" {
		t.Fatalf("%q %q", fo.gotRedirect, fo.gotChallenge)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")

	res, err := http.Get(fo.gotRedirect + "?code=good&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a forged state: %d", res.StatusCode)
	}
	if done, _, _ := s.OAuthStatus(flow); done {
		t.Fatal("a forged state must not finish the flow")
	}

	res, err = http.Get(fo.gotRedirect + "?code=good&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "authorized") {
		t.Fatalf("%s", body)
	}
	done, msg, err := s.OAuthStatus(flow)
	if err != nil || !done || msg != "" {
		t.Fatalf("%v %q %v", done, msg, err)
	}
	if !s.Store.HasSecret(c) {
		t.Fatal("the tokens must be stored")
	}
	raw, _ := s.Store.Secrets.Get(c.ID)
	var creds Credentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil || creds.RefreshToken != "r" {
		t.Fatalf("%v %v", creds, err)
	}
	if _, _, err := s.OAuthStatus("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestFreeTextTerms(t *testing.T) {
	got := strings.Join(FreeTextTerms(`project: HELP canary "exact phrase" #Unresolved tag:x assignee: {John Doe} word`), "|")
	if got != "exact phrase|canary|word" {
		t.Fatal(got)
	}
	if s := Snippet("a\nb canary\nc", []string{"canary"}); s != "b canary" {
		t.Fatal(s)
	}
}
