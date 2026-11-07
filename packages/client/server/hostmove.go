package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/relay/v1/relayv1connect"
	"github.com/xlsft/harness/packages/client/hostgit"
	"github.com/xlsft/harness/packages/client/pbconv"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/vcs"
	"github.com/xlsft/harness/packages/client/workspace"
)

// Moving a live session to an always-on host (docs/design/hosts.md, decisions 21 to 27). The laptop writes the working tree
// as one commit through a temporary index, so the user's checkout is not touched; pushes it to harness/<session id> with the
// credentials the host would get; and queues the work, which the host claims like any other: it checks out the branch and
// forks the server's transcript. The session here becomes read-only.

const (
	// moveStopWait bounds how long a move waits for the turn it stopped.
	moveStopWait   = 30 * time.Second
	moveGitTimeout = 30 * time.Second
	// movedListCap keeps a refusal's lists readable.
	movedListCap = 20
)

// hostsAPI reaches the server's host services with this client's sign-in.
func (s *Server) hostsAPI() (relayv1connect.HostsServiceClient, relayv1connect.HostAgentServiceClient, rpc.Tokens, error) {
	if s.Core == nil || s.Core.URL == "" {
		return nil, nil, nil, errors.New("this client is not signed in to a server")
	}
	hc := s.Core.HTTP
	if hc == nil {
		hc = rpc.HTTPClient(s.Core.URL)
	}
	base := strings.TrimRight(s.Core.URL, "/")
	return relayv1connect.NewHostsServiceClient(hc, base, rpc.ClientOptions()...), relayv1connect.NewHostAgentServiceClient(hc, base, rpc.ClientOptions()...), s.Core.Tokens, nil
}

// coreCall runs one call to the server with the sign-in's token, refreshing it once.
func coreCall[Req, Res any](ctx context.Context, tokens rpc.Tokens, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) (*Res, error) {
	do := func(token string) (*Res, error) {
		req := connect.NewRequest(msg)
		if token != "" {
			rpc.SetBearer(req.Header(), token)
		}
		res, err := fn(ctx, req)
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if tokens == nil {
		return do("")
	}
	token, err := tokens.Token(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	out, err := do(token)
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		if token, rerr := tokens.Refresh(ctx); rerr == nil {
			out, err = do(token)
		}
	}
	return out, err
}

// moveGit runs a git command of the user's own checkout that needs no credentials: their identity and their configuration,
// without hooks.
func moveGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, moveGitTimeout)
	defer cancel()
	full := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0"), env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = err.Error()
		}
		return string(out), fmt.Errorf("git %s: %s", args[0], msg)
	}
	return string(out), nil
}

// movePlan is what a move takes, found by checkMove.
type movePlan struct {
	ls      *liveSession
	hostID  string
	host    string
	top     string
	repo    vcs.Repo
	base    string
	branch  string
	mode    agentv1.PermissionMode
	title   string
	model   string
	effort  string
	working bool
}

func moveProblem(code, msg string, paths ...string) *agentv1.SessionMoveProblem {
	if len(paths) > movedListCap {
		paths = append(paths[:movedListCap:movedListCap], fmt.Sprintf("and %d more", len(paths)-movedListCap))
	}
	return &agentv1.SessionMoveProblem{Code: code, Message: msg, Paths: paths}
}

// secretNames are files that hold secrets wherever they are; a deny rule on Read covers the rest (decision 25).
var secretNames = map[string]bool{
	".env": true, ".netrc": true, "_netrc": true, ".npmrc": true, ".pypirc": true, ".git-credentials": true, ".pgpass": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true, ".htpasswd": true,
}

var secretExts = map[string]bool{".pem": true, ".key": true, ".p12": true, ".pfx": true, ".jks": true, ".keystore": true}

// secretFile reports whether a path names a well-known secret file. Examples and templates of an environment file are not.
func secretFile(rel string) bool {
	name := strings.ToLower(path.Base(filepath.ToSlash(rel)))
	if secretNames[name] || secretExts[path.Ext(name)] {
		return true
	}
	if rest, ok := strings.CutPrefix(name, ".env."); ok {
		switch rest {
		case "example", "sample", "template", "dist", "defaults", "schema":
			return false
		}
		return true
	}
	return false
}

// moveMode is the host session's mode: the session's, bypass excepted (decision 27).
func moveMode(ls *liveSession) (agentv1.PermissionMode, string) {
	m := ls.sess.Permissions.Mode()
	if m != permissions.ModeBypassPermissions {
		return modeToProto(m), ""
	}
	if ls.sess.Permissions.ModeAllowed(permissions.ModeAuto) {
		return agentv1.PermissionMode_PERMISSION_MODE_AUTO, "Bypass is never granted remotely: the host session runs in auto mode"
	}
	return agentv1.PermissionMode_PERMISSION_MODE_DEFAULT, "Bypass is never granted remotely: the host session asks for permission"
}

// checkMove finds out whether the session can move, without changing anything.
func (s *Server) checkMove(ctx context.Context, sessionID, hostID string) (*agentv1.SessionMoveCheck, *movePlan, error) {
	out := &agentv1.SessionMoveCheck{}
	add := func(p *agentv1.SessionMoveProblem) { out.Problems = append(out.Problems, p) }
	ls, err := s.get(sessionID)
	if err != nil {
		if s.remoteInfo(sessionID) != nil {
			add(moveProblem("remote", "This session runs on another machine; only a session of this one can move from here"))
			return out, nil, nil
		}
		return nil, nil, err
	}
	plan := &movePlan{ls: ls, hostID: hostID, branch: "harness/" + sessionID}
	out.Branch = plan.branch
	if mv := ls.movedTo(); mv != nil {
		add(moveProblem("moved", fmt.Sprintf("This session moved to %s already", mv.GetHostName())))
		return out, nil, nil
	}
	if isNoFolder(ls.sess.ID, ls.sess.Dir()) {
		add(moveProblem("no_folder", "A chat without a folder has no work to move"))
		return out, nil, nil
	}
	out.Working = ls.info().GetStatus() != agentv1.SessionStatus_SESSION_STATUS_IDLE
	plan.working = out.Working
	out.PermissionMode, out.PermissionNote = moveMode(ls)
	plan.mode = out.PermissionMode
	hosts, agents, tokens, err := s.hostsAPI()
	_ = agents
	if err != nil {
		add(moveProblem("no_server", "Moving needs the server: sign in first"))
	} else if res, err := coreCall(ctx, tokens, hosts.ListAgentHosts, &relayv1.ListAgentHostsRequest{}); err != nil {
		add(moveProblem("host", "The server did not list your hosts: "+connectMessage(err)))
	} else {
		var host *relayv1.AgentHost
		for _, h := range res.GetHosts() {
			if h.GetId() == hostID {
				host = h
			}
		}
		switch {
		case host == nil:
			add(moveProblem("host", "That host is not one of yours"))
		case host.GetUnavailableReason() != "":
			add(moveProblem("host", host.GetUnavailableReason()))
		default:
			out.HostName, plan.host = host.GetName(), host.GetName()
		}
	}
	if ls.sess.Uploader == nil {
		add(moveProblem("no_transcript", "Sync is off, so the server has no transcript for the host to continue from"))
	}
	top, err := moveGit(ctx, ls.sess.Dir(), nil, "rev-parse", "--show-toplevel")
	if err != nil {
		add(moveProblem("not_git", "This folder is not in a git repository; only a branch can move"))
		return out, nil, nil
	}
	plan.top = strings.TrimSpace(top)
	remote, _ := moveGit(ctx, plan.top, nil, "remote", "get-url", "origin")
	repo, ok := vcs.ParseRemote(strings.TrimSpace(remote))
	if !ok {
		add(moveProblem("no_remote", "The repository has no origin on a code host to push the branch to"))
		return out, nil, nil
	}
	plan.repo, out.RepositoryKey = repo, repo.Key
	if (hostgit.Git{}).RewritesURLs(ctx, plan.top) {
		add(moveProblem("url_rewrite", "The repository's configuration rewrites remote URLs; nothing is pushed with credentials from it"))
	}
	base := workspace.DefaultBranch(ctx, plan.top)
	if base == "" {
		base = "main"
	}
	plan.base, out.Base = base, base
	baseRef := "origin/" + base
	if _, err := moveGit(ctx, plan.top, nil, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}"); err != nil {
		add(moveProblem("base_unknown", fmt.Sprintf("%s is not known here: fetch it first, so that what goes up can be told from what is there", baseRef)))
		return out, plan, nil
	}
	if _, err := moveGit(ctx, plan.top, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		add(moveProblem("detached", "The repository has no commit yet"))
		return out, plan, nil
	}
	if n, err := moveGit(ctx, plan.top, nil, "rev-list", "--count", baseRef+"..HEAD"); err == nil {
		c, _ := strconv.Atoi(strings.TrimSpace(n))
		out.Commits = int32(c)
	}
	// What goes up: the commits on top of the base, and what is not committed.
	files := map[string]bool{}
	if mb, err := moveGit(ctx, plan.top, nil, "merge-base", baseRef, "HEAD"); err == nil {
		if names, err := moveGit(ctx, plan.top, nil, "diff", "--name-only", "--diff-filter=d", "-z", strings.TrimSpace(mb), "HEAD"); err == nil {
			for _, f := range strings.Split(names, "\x00") {
				if f != "" {
					files[f] = true
				}
			}
		}
	}
	status, err := moveGit(ctx, plan.top, nil, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	var submodules []string
	uncommitted := 0
	for _, rec := range splitStatusV2(status) {
		if rec.submodule {
			submodules = append(submodules, rec.path)
			continue
		}
		uncommitted++
		if !rec.deleted {
			files[rec.path] = true
		}
	}
	out.UncommittedFiles = int32(uncommitted)
	if len(submodules) > 0 {
		add(moveProblem("submodule", "A submodule has changes or commits of its own, which the host could not fetch", submodules...))
	}
	var secrets []string
	for f := range files {
		abs := filepath.Join(plan.top, filepath.FromSlash(f))
		if secretFile(f) || ls.sess.Permissions.DeniesRead(abs) != "" {
			secrets = append(secrets, f)
		}
	}
	sort.Strings(secrets)
	if len(secrets) > 0 {
		add(moveProblem("secrets", "These files would go up and look like secrets, or a deny rule keeps them from being read: remove them, or add them to .gitignore", secrets...))
	}
	if outside := s.changesOutside(ctx, ls, plan.top); len(outside) > 0 {
		add(moveProblem("outside_changes", "The session changed files outside this repository, which a branch can't carry", outside...))
	}
	plan.title = ls.info().GetTitle()
	plan.model = ls.sess.Agent.Model()
	out.Ok = len(out.Problems) == 0
	return out, plan, nil
}

// statusEntry is one record of `git status --porcelain=v2 -z`.
type statusEntry struct {
	path      string
	deleted   bool
	submodule bool
}

// splitStatusV2 reads the records that matter for a move: changed and untracked paths, and submodules with anything in them.
func splitStatusV2(out string) []statusEntry {
	var list []statusEntry
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		switch {
		case strings.HasPrefix(r, "? "):
			list = append(list, statusEntry{path: r[2:]})
		case strings.HasPrefix(r, "1 "), strings.HasPrefix(r, "u "):
			f := strings.SplitN(r, " ", 9)
			if r[0] == 'u' {
				f = strings.SplitN(r, " ", 11)
			}
			if len(f) < 9 {
				continue
			}
			list = append(list, entryOf(f[1], f[2], f[len(f)-1]))
		case strings.HasPrefix(r, "2 "):
			f := strings.SplitN(r, " ", 10)
			if len(f) < 10 {
				continue
			}
			list = append(list, entryOf(f[1], f[2], f[9]))
			i++ // the original path follows
		}
	}
	return list
}

func entryOf(xy, sub, p string) statusEntry {
	// sub is "N..." for a file, "S<c><m><u>" for a submodule: commit changed, modified content, untracked content.
	if strings.HasPrefix(sub, "S") && sub != "S..." {
		return statusEntry{path: p, submodule: true}
	}
	return statusEntry{path: p, deleted: strings.Contains(xy, "D")}
}

// changesOutside lists what the session changed outside the repository top, its scratch directory excepted.
func (s *Server) changesOutside(ctx context.Context, ls *liveSession, top string) []string {
	src := ls.diffSources()
	res, err := computeSessionDiff(ctx, src, nil, "")
	if err != nil || res == nil {
		return nil
	}
	realTop := top
	if r, err := filepath.EvalSymlinks(top); err == nil {
		realTop = r
	}
	var out []string
	for _, f := range res.GetFiles() {
		p := f.GetPath()
		if p == "" || pathInside(p, top) || pathInside(p, realTop) || (src.scratch != "" && pathInside(p, src.scratch)) {
			continue
		}
		if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil && pathInside(filepath.Join(r, filepath.Base(p)), realTop) {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (s *Server) CheckSessionMove(ctx context.Context, req *connect.Request[agentv1.CheckSessionMoveRequest]) (*connect.Response[agentv1.CheckSessionMoveResponse], error) {
	if req.Msg.GetHostId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("host_id is required"))
	}
	check, _, err := s.checkMove(ctx, req.Msg.GetSessionId(), req.Msg.GetHostId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.CheckSessionMoveResponse{Check: check}), nil
}

func moveRefusal(check *agentv1.SessionMoveCheck) error {
	var parts []string
	for _, p := range check.GetProblems() {
		m := p.GetMessage()
		if len(p.GetPaths()) > 0 {
			m += ": " + strings.Join(p.GetPaths(), ", ")
		}
		parts = append(parts, m)
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the session can't move: %s", strings.Join(parts, "; ")))
}

func (s *Server) MoveSession(ctx context.Context, req *connect.Request[agentv1.MoveSessionRequest]) (*connect.Response[agentv1.MoveSessionResponse], error) {
	m := req.Msg
	if m.GetHostId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("host_id is required"))
	}
	check, plan, err := s.checkMove(ctx, m.GetSessionId(), m.GetHostId())
	if err != nil {
		return nil, err
	}
	if !check.GetOk() || plan == nil {
		return nil, moveRefusal(check)
	}
	ls := plan.ls
	if plan.working {
		if !m.GetStopTurn() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session is in a turn: stop it, or let the move stop it"))
		}
		ls.driver.Interrupt()
		deadline := time.Now().Add(moveStopWait)
		for ls.driver.Busy() && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		if ls.driver.Busy() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the turn did not stop in time; try again"))
		}
	}
	hosts, agents, tokens, err := s.hostsAPI()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	// The whole transcript on the server first: the host forks what is there.
	if err := ls.sess.Uploader.Flush(ctx, ls.sess.ID); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("the transcript did not reach the server: %w", err))
	}
	commit, err := s.moveCommit(ctx, plan)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	landing := relayv1.HostWorkLanding_HOST_WORK_LANDING_BRANCH
	if m.GetOpenPullRequest() {
		landing = relayv1.HostWorkLanding_HOST_WORK_LANDING_PULL_REQUEST
	}
	started, err := coreCall(ctx, tokens, hosts.StartHostWork, &relayv1.StartHostWorkRequest{
		HostId: plan.hostID, RepositoryKey: plan.repo.Key, BaseRef: plan.base, Prompt: m.GetMessage(), Title: plan.title, Model: plan.model,
		Effort: pbconv.Effort(ls.sess.Agent.Effort()), PermissionMode: plan.mode, Landing: landing, Kind: relayv1.HostWorkKind_HOST_WORK_KIND_MOVE,
		Branch: plan.branch, ForkSessionId: ls.sess.ID, IdempotencyKey: "move:" + ls.sess.ID + ":" + commit,
	})
	if err != nil {
		return nil, err
	}
	work := started.GetWork()
	cancelWork := func() {
		if _, err := coreCall(context.WithoutCancel(ctx), tokens, hosts.CancelHostWork, &relayv1.CancelHostWorkRequest{Id: work.GetId()}); err != nil {
			s.log().Warn("cancelling a move that did not push failed", "work", work.GetId(), "err", err)
		}
	}
	cred, err := coreCall(ctx, tokens, agents.IssueMoveCredentials, &relayv1.IssueMoveCredentialsRequest{WorkId: work.GetId()})
	if err != nil {
		cancelWork()
		return nil, err
	}
	c := cred.GetCredentials()
	if _, err := s.HostGit.PushCommit(ctx, plan.top, commit, plan.branch, plan.base, hostgit.Credentials{CloneURL: c.GetCloneUrl(), Username: c.GetUsername(), Password: c.GetPassword()}); err != nil {
		cancelWork()
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("pushing %s failed: %w", plan.branch, err))
	}
	if _, err := coreCall(ctx, tokens, agents.QueueMovedWork, &relayv1.QueueMovedWorkRequest{WorkId: work.GetId(), Head: commit}); err != nil {
		cancelWork()
		return nil, err
	}
	moved := &agentv1.SessionMoved{HostId: plan.hostID, HostName: plan.host, WorkId: work.GetId(), Branch: plan.branch, MovedAt: time.Now().UTC().Format(time.RFC3339)}
	ls.markMoved(moved)
	return connect.NewResponse(&agentv1.MoveSessionResponse{Moved: moved}), nil
}

// moveCommit writes the working tree as one commit on top of HEAD through a temporary index (decision 21): .gitignore
// holds, and the user's index, branch and files stay as they are. With nothing uncommitted it is HEAD.
func (s *Server) moveCommit(ctx context.Context, plan *movePlan) (string, error) {
	head, err := moveGit(ctx, plan.top, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	indexPath, err := moveGit(ctx, plan.top, nil, "rev-parse", "--path-format=absolute", "--git-path", "harness-move-index")
	if err != nil {
		return "", err
	}
	index := strings.TrimSpace(indexPath)
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := moveGit(ctx, plan.top, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := moveGit(ctx, plan.top, env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	tree, err := moveGit(ctx, plan.top, env, "write-tree")
	if err != nil {
		return "", err
	}
	headTree, err := moveGit(ctx, plan.top, nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(tree) == strings.TrimSpace(headTree) {
		return head, nil
	}
	var who []string
	if email, _ := moveGit(ctx, plan.top, nil, "config", "user.email"); strings.TrimSpace(email) == "" && s.UserEmail != "" {
		who = []string{"GIT_AUTHOR_NAME=" + s.UserEmail, "GIT_AUTHOR_EMAIL=" + s.UserEmail, "GIT_COMMITTER_NAME=" + s.UserEmail, "GIT_COMMITTER_EMAIL=" + s.UserEmail}
	}
	msg := "Work in progress of a session, moved to " + plan.host
	if plan.title != "" {
		msg = plan.title + "\n\nWork in progress, moved to the always-on host " + plan.host + "."
	}
	commit, err := moveGit(ctx, plan.top, who, "commit-tree", strings.TrimSpace(tree), "-p", head, "-m", msg)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(commit), nil
}

// ---- the read-only copy ----

// movedTo is where the session went, nil when it is still here.
func (ls *liveSession) movedTo() *agentv1.SessionMoved {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.moved
}

// readOnly is the refusal of anything that would change a moved session.
func (ls *liveSession) readOnly() error {
	if mv := ls.movedTo(); mv != nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this session moved to %s and is read-only here; continue it there", mv.GetHostName()))
	}
	return nil
}

// markMoved records the move in the transcript and tells the viewers.
func (ls *liveSession) markMoved(mv *agentv1.SessionMoved) {
	ls.mu.Lock()
	ls.moved = mv
	ls.autoContinue = false
	ls.mu.Unlock()
	rec := &session.MovedTo{Session: ls.sess.ID, HostID: mv.GetHostId(), HostName: mv.GetHostName(), WorkID: mv.GetWorkId(), Branch: mv.GetBranch(), At: mv.GetMovedAt()}
	if _, err := ls.sess.Transcript.Append(session.Entry{Type: session.TypeMeta, SessionID: ls.sess.ID, MovedTo: rec, Timestamp: mv.GetMovedAt()}); err != nil {
		ls.srv.log().Warn("recording the move in the transcript failed", "session", ls.sess.ID, "err", err)
	}
	ls.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionMoved{SessionMoved: mv}})
	ls.publishInfo()
	if up := ls.sess.Uploader; up != nil {
		up.Notify(ls.sess.ID)
	}
}

// restoreMoved makes a session that moved read-only again when it opens (decision 24).
func (ls *liveSession) restoreMoved(entries []session.Entry) {
	rec := session.ReadMovedTo(entries, ls.sess.ID)
	if rec == nil {
		return
	}
	mv := &agentv1.SessionMoved{HostId: rec.HostID, HostName: rec.HostName, WorkId: rec.WorkID, Branch: rec.Branch, MovedAt: rec.At}
	ls.mu.Lock()
	ls.moved = mv
	ls.autoContinue = false
	ls.mu.Unlock()
	ls.hub.publish(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_SessionMoved{SessionMoved: mv}})
	ls.history++
}

// runMoveCommand is /move: it opens the confirmation; nothing moves before the user confirms (decision 26).
func runMoveCommand(context.Context, *Server, *liveSession, string) slashOutcome {
	return slashOutcome{action: agentv1.SlashCommandResult_ACTION_OPEN_MOVE}
}
