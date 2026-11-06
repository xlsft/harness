package vcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	vcsv1 "github.com/xlsft/harness/packages/client/gen/harness/vcs/v1"
	"github.com/xlsft/harness/packages/client/rpc"
)

// ServerProvider reaches code hosts through the harness server's connections (VcsService): the token stays on the
// server. As a Tracker provider it goes before gh and glab: "when a server provider and a device provider cover the same
// repository, the server provider wins". It only claims the hosts where the server can act for this user.
type ServerProvider struct {
	c *Client
}

var (
	_ Provider   = (*ServerProvider)(nil)
	_ PRProvider = (*ServerProvider)(nil)
	_ PRCreator  = (*ServerProvider)(nil)
)

var (
	serverProvidersMu sync.Mutex
	serverProviders   = map[string]*ServerProvider{}
)

// SharedServerProvider returns the one provider of a server, so the detection cache works across sessions.
func SharedServerProvider(baseURL string, httpClient *http.Client, tokens rpc.Tokens) *ServerProvider {
	serverProvidersMu.Lock()
	defer serverProvidersMu.Unlock()
	if p := serverProviders[baseURL]; p != nil {
		return p
	}
	p := &ServerProvider{c: NewClient(baseURL, httpClient, tokens)}
	serverProviders[baseURL] = p
	return p
}

// WithServerProvider is the Tracker's provider list with the server first.
func WithServerProvider(p *ServerProvider) []Provider {
	return append([]Provider{p}, defaultProviders...)
}

// Type is the one a device provider never has; the name in messages comes from providerLabel.
func (p *ServerProvider) Type() vcsv1.ProviderType {
	return vcsv1.ProviderType_PROVIDER_TYPE_UNSPECIFIED
}

// Detect lists the hosts where the server can act for this user. A server without the RPC, or a user who is signed
// out, simply covers nothing, so the CLIs take over.
func (p *ServerProvider) Detect(ctx context.Context) (*vcsv1.DeviceProvider, error) {
	d := &vcsv1.DeviceProvider{Type: p.Type(), Version: "harness server"}
	res, err := unary(ctx, p.c, &vcsv1.ListUserConnectionsRequest{}, p.c.rpc.ListUserConnections)
	if err != nil {
		return d, nil
	}
	for _, c := range res.GetConnections() {
		if c.GetCanWrite() && c.GetHost() != "" {
			d.Hosts = append(d.Hosts, &vcsv1.DeviceProviderHost{Host: strings.ToLower(c.GetHost()), Login: c.GetLogin()})
		}
	}
	sortHosts(d.Hosts)
	return d, nil
}

// Snapshot lists the branch's PRs with the checks of the most relevant one.
func (p *ServerProvider) Snapshot(ctx context.Context, repo Repo, branch string) (*Snapshot, error) {
	if branch == "" {
		return nil, errors.New("no branch")
	}
	res, err := unary(ctx, p.c, &vcsv1.ListPullRequestsRequest{RepositoryKey: repo.Key, HeadBranch: branch, IncludeClosed: true}, p.c.rpc.ListPullRequests)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{PullRequests: res.GetPullRequests(), Checks: map[string]*vcsv1.ChecksList{}}
	sortPullRequests(snap.PullRequests)
	if len(snap.PullRequests) > 0 {
		top := snap.PullRequests[0]
		checks, err := unary(ctx, p.c, &vcsv1.ListChecksRequest{RepositoryKey: repo.Key, Number: top.GetNumber()}, p.c.rpc.ListChecks)
		if err == nil {
			snap.Checks[CheckKey(repo.Key, top.GetNumber())] = &vcsv1.ChecksList{Checks: checks.GetChecks()}
		}
	}
	return snap, nil
}

// ViewPR reads the PR and its checks.
func (p *ServerProvider) ViewPR(ctx context.Context, repo Repo, number int64) (*PRView, error) {
	res, err := unary(ctx, p.c, &vcsv1.GetPullRequestRequest{RepositoryKey: repo.Key, Number: number}, p.c.rpc.GetPullRequest)
	if err != nil {
		return nil, err
	}
	view := &PRView{PR: res.GetPullRequest()}
	checks, err := unary(ctx, p.c, &vcsv1.ListChecksRequest{RepositoryKey: repo.Key, Number: number}, p.c.rpc.ListChecks)
	if err != nil {
		return nil, err
	}
	view.Checks = checks.GetChecks()
	if view.PR != nil && len(view.Checks) > 0 {
		view.PR.Checks = summarize(view.Checks)
	}
	return view, nil
}

// PRComments lists the review comments; a comment with a path and not resolved counts as an open thread.
func (p *ServerProvider) PRComments(ctx context.Context, repo Repo, number int64) ([]Comment, int, error) {
	res, err := unary(ctx, p.c, &vcsv1.ListReviewCommentsRequest{RepositoryKey: repo.Key, Number: number}, p.c.rpc.ListReviewComments)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Comment, 0, len(res.GetComments()))
	unresolved := 0
	for _, c := range res.GetComments() {
		out = append(out, Comment{ID: c.GetId(), Author: c.GetAuthor(), Body: c.GetBody(), Path: c.GetPath(), Line: int(c.GetLine()), URL: c.GetUrl(), CreatedAt: c.GetCreatedAt(), Resolved: c.GetResolved()})
		if c.GetPath() != "" && !c.GetResolved() {
			unresolved++
		}
	}
	return out, unresolved, nil
}

// CheckLog reads a check's log tail.
func (p *ServerProvider) CheckLog(ctx context.Context, repo Repo, check *vcsv1.Check) (string, error) {
	res, err := unary(ctx, p.c, &vcsv1.GetCheckLogRequest{RepositoryKey: repo.Key, CheckId: check.GetId()}, p.c.rpc.GetCheckLog)
	if err != nil {
		return "", err
	}
	return res.GetLog(), nil
}

// SetAutoMerge asks the server.
func (p *ServerProvider) SetAutoMerge(ctx context.Context, repo Repo, number int64, enabled bool, method string) error {
	_, err := unary(ctx, p.c, &vcsv1.SetAutoMergeRequest{RepositoryKey: repo.Key, Number: number, Enabled: enabled, Method: method}, p.c.rpc.SetAutoMerge)
	return err
}

// CreatePR asks the server, linking the session when the server knows it. A session the server has not seen (sync off,
// not yet uploaded) is no reason to fail: the PR is opened without the link.
func (p *ServerProvider) CreatePR(ctx context.Context, repo Repo, in PRInput) (*vcsv1.PullRequest, error) {
	req := &vcsv1.CreatePullRequestRequest{RepositoryKey: repo.Key, HeadBranch: in.Head, BaseBranch: in.Base, Title: in.Title, Body: in.Body, Draft: in.Draft, SessionId: in.SessionID}
	res, err := unary(ctx, p.c, req, p.c.rpc.CreatePullRequest)
	if err != nil && in.SessionID != "" && connectCode(err) == connect.CodeNotFound {
		req.SessionId = ""
		res, err = unary(ctx, p.c, req, p.c.rpc.CreatePullRequest)
	}
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, ErrPRExists
		}
		return nil, fmt.Errorf("the harness server could not open the pull request: %w", err)
	}
	return res.GetPullRequest(), nil
}

// CLIStatus says what the local CLIs can do, for the settings screen. providers defaults to gh and glab; anything that is
// not a CLI is left out. It always detects afresh: someone may have just signed in.
func CLIStatus(ctx context.Context, providers []Provider) []*agentv1.VcsDeviceCli {
	if len(providers) == 0 {
		providers = defaultProviders
	}
	var out []*agentv1.VcsDeviceCli
	for _, p := range providers {
		var cli *agentv1.VcsDeviceCli
		switch p.Type() {
		case vcsv1.ProviderType_PROVIDER_TYPE_GH:
			cli = &agentv1.VcsDeviceCli{Name: "gh", LoginCommand: "gh auth login"}
		case vcsv1.ProviderType_PROVIDER_TYPE_GLAB:
			cli = &agentv1.VcsDeviceCli{Name: "glab", LoginCommand: "glab auth login"}
		default:
			continue
		}
		d, err := p.Detect(ctx)
		switch {
		case errors.Is(err, ErrNotInstalled):
		case err != nil:
			cli.Installed, cli.Problem = true, err.Error()
		default:
			cli.Installed, cli.Version = true, d.GetVersion()
			for _, h := range d.GetHosts() {
				cli.Hosts = append(cli.Hosts, &agentv1.VcsDeviceCliHost{Host: h.GetHost(), Login: h.GetLogin()})
			}
		}
		out = append(out, cli)
	}
	return out
}

func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	if strings.Contains(err.Error(), "not_found") || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return connect.CodeNotFound
	}
	return connect.CodeUnknown
}
