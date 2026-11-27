package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	relayv1 "github.com/xlsft/harness/packages/client/gen/harness/relay/v1"
)

// A cloud session's container (docs/design/cloud-sessions.md): `harness-client daemon --cloud`, signed in with the
// session's own device. It takes one piece of work, its session's, which is new the first time and to resume every time
// after (decision 8): the branch from the code host, the transcript from the server, the same session id.

// resumeCloudWork brings back a session whose container was removed. The work row says the session and its branch.
func (r *Runner) resumeCloudWork(ctx context.Context, w *relayv1.HostWork, gitName, gitEmail string) error {
	cred, err := r.credentials(ctx, w.GetRepositoryKey())
	if err != nil {
		return err
	}
	branch := w.GetBranch()
	if branch == "" {
		branch = "harness/" + w.GetId()
	}
	// The first turn may have left nothing to push: then the branch starts again from the base.
	existing, err := r.o.Git.RemoteHasBranch(ctx, r.o.Root, branch, cred)
	if err != nil {
		return err
	}
	dir := filepath.Join(r.o.Root, "work", w.GetId())
	p, err := r.o.Git.Prepare(ctx, r.o.Root, Checkout{
		RepositoryKey: w.GetRepositoryKey(), Base: w.GetBaseRef(), Branch: branch, Existing: existing, Fresh: true, Dir: dir, Name: gitName, Email: gitEmail,
	}, cred)
	if err != nil {
		return err
	}
	if r.o.Setup != nil {
		if err := r.o.Setup(ctx, p.Dir); err != nil {
			r.log.Warn("the project's setup failed; the session goes on without it", "err", err)
		}
	}
	id := w.GetSessionId()
	if r.o.Restore == nil {
		return errors.New("this daemon can't read transcripts from the server")
	}
	if err := r.o.Restore(ctx, id, p.Dir); err != nil {
		return err
	}
	if _, err := r.o.Sessions.Create(ctx, &agentv1.CreateSessionRequest{ResumeSessionId: id, Cwd: p.Dir}); err != nil {
		return err
	}
	t := &tracked{SessionID: id, WorkID: w.GetId(), RepositoryKey: w.GetRepositoryKey(), Dir: p.Dir, Branch: p.Branch, Base: p.Base, Landing: int32(cloudLanding(w.GetLanding())), Title: w.GetTitle(), PullRequest: w.GetPullRequestUrl()}
	r.track(t)
	r.update(ctx, &relayv1.UpdateHostWorkRequest{WorkId: w.GetId(), State: relayv1.HostWorkState_HOST_WORK_STATE_RUNNING, SessionId: id, Branch: p.Branch})
	r.startWatching(ctx, id)
	r.kickReport()
	r.log.Info("resumed a cloud session", "session", id, "branch", p.Branch)
	return nil
}

// cloudLanding: the branch is where a cloud session's state lives, so it is always pushed (decision 9).
func cloudLanding(l relayv1.HostWorkLanding) relayv1.HostWorkLanding {
	if l == relayv1.HostWorkLanding_HOST_WORK_LANDING_NONE {
		return relayv1.HostWorkLanding_HOST_WORK_LANDING_BRANCH
	}
	return l
}

// RemoteHasBranch reports whether the code host has the branch.
func (g Git) RemoteHasBranch(ctx context.Context, dir, branch string, cred Credentials) (bool, error) {
	if err := checkRefName(branch); err != nil {
		return false, err
	}
	out, err := g.run(ctx, dir, gitNetTimeout, &cred, nil, "ls-remote", "--heads", cred.CloneURL, "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}
