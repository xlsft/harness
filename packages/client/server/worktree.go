package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	"github.com/xlsft/harness/packages/client/config"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/hooks"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/vcshooks"
	"github.com/xlsft/harness/packages/client/worktree"
)

// createWorktree creates the worktree requested by CreateSession (nil when
// none was requested) and returns the session metadata to record. When the
// folder's settings have WorktreeCreate hooks, one of them makes the copy
// instead of git (docs/design/vcs-hooks.md).
func (s *Server) createWorktree(ctx context.Context, m *agentv1.CreateSessionRequest, sessionID string) (*worktree.Result, *session.Worktree, error) {
	o := m.GetWorktree()
	if o == nil {
		return nil, nil, nil
	}
	if m.GetCwd() == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cwd is required"))
	}
	if h, release, err := s.folderVcsHooks(ctx, m.GetCwd(), sessionID); err != nil {
		return nil, nil, err
	} else if h.Isolates() {
		defer release()
		return createHookWorktree(ctx, h, m, o)
	} else {
		release()
	}
	r, err := worktree.Create(ctx, m.GetCwd(), worktree.Options{Name: o.GetName(), Branch: o.GetBranch(), BaseRef: o.GetBaseRef(), PullRequest: o.GetFromPr()})
	if err != nil {
		code := connect.CodeFailedPrecondition
		switch {
		case errors.Is(err, worktree.ErrInvalidName), errors.Is(err, worktree.ErrInvalidRef):
			code = connect.CodeInvalidArgument
		case errors.Is(err, worktree.ErrNotRepository):
			err = errors.New("cwd is not inside a git repository; a worktree needs one")
		}
		return nil, nil, connect.NewError(code, err)
	}
	return r, &session.Worktree{Path: r.Path, Branch: r.Branch, BaseRef: r.BaseRef, RepositoryPath: r.RepositoryPath}, nil
}

// createHookWorktree is createWorktree through the folder's WorktreeCreate hooks. A failure names the hook: the frontend
// shows it and offers to start in the folder itself, which happens only when the user picks that.
func createHookWorktree(ctx context.Context, h vcshooks.Hooks, m *agentv1.CreateSessionRequest, o *agentv1.WorktreeOptions) (*worktree.Result, *session.Worktree, error) {
	if o.GetFromPr() != "" || o.GetBranch() != "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this folder makes its working copies through WorktreeCreate hooks, which take no branch or pull request"))
	}
	c, err := h.Create(ctx, vcshooks.CreateRequest{Source: m.GetCwd(), Name: o.GetName(), Kind: vcshooks.KindSession, BaseRevision: o.GetBaseRef()})
	if err != nil {
		code := connect.CodeFailedPrecondition
		if errors.Is(err, worktree.ErrInvalidName) {
			code = connect.CodeInvalidArgument
		}
		return nil, nil, connect.NewError(code, err)
	}
	r := &worktree.Result{Name: c.Name, Path: c.Path, Cwd: c.Cwd, BaseRef: c.Base, RepositoryPath: c.Source, Hook: true}
	return r, &session.Worktree{Path: c.Path, BaseRef: c.Base, RepositoryPath: c.Source, Hook: true, Name: c.Name}, nil
}

// folderVcsHooks are the VCS hooks a new session in cwd would run, before it opens: the folder's settings under the same
// gates (workspace trust, this device's trust in what the server delivered, a policy's allowManagedHooksOnly). When the
// server's documents cannot be read, the policy cannot be either: hooks that would isolate are then refused rather than run
// unchecked. release closes the engine.
func (s *Server) folderVcsHooks(ctx context.Context, cwd, sessionID string) (vcshooks.Hooks, func(), error) {
	none := func() {}
	abs, err := absDir(cwd)
	if err != nil {
		return vcshooks.Hooks{}, none, nil
	}
	docsCtx, cancel := context.WithTimeout(ctx, sessionSyncTimeout)
	docs, docsErr := s.hookDocs(docsCtx)
	cancel()
	st, err := settings.Load(ctx, settings.Options{Cwd: abs, Docs: docs, Trusted: config.IsChatDir(abs) || permissions.IsTrusted(abs)})
	if err != nil {
		return vcshooks.Hooks{}, none, nil
	}
	e := hooks.New(bootstrap.BuildHooks(st, abs), hooks.Options{SessionID: sessionID, Cwd: func() string { return abs }, ProjectDir: func() string { return abs }, Logger: s.log()})
	h := vcshooks.Of(e)
	if docsErr != nil && h.Isolates() {
		e.Close()
		return vcshooks.Hooks{}, none, connect.NewError(connect.CodeUnavailable, fmt.Errorf("WorktreeCreate hooks are configured, but the server's policy could not be read to check them: %w", docsErr))
	}
	return h, e.Close, nil
}

// rollbackWorktree removes a worktree created for a session that failed to
// start; nothing has run in it yet.
func (s *Server) rollbackWorktree(r *worktree.Result, cwd, sessionID string) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktree.AddTimeout)
	defer cancel()
	if r.Hook {
		h, release, err := s.folderVcsHooks(ctx, cwd, sessionID)
		if err == nil {
			err = h.Remove(ctx, vcshooks.RemoveRequest{Path: r.Path, Source: r.RepositoryPath, Name: r.Name, Kind: vcshooks.KindSession, Force: true})
		}
		release()
		if err != nil && !errors.Is(err, vcshooks.ErrNoRemoveHook) {
			s.log().Warn("remove working copy of a failed session", "path", r.Path, "err", err)
		}
		return
	}
	if err := worktree.Rollback(ctx, r); err != nil {
		s.log().Warn("remove worktree of a failed session", "path", r.Path, "err", err)
	}
}

func worktreeToProto(w *session.Worktree) *agentv1.Worktree {
	if w == nil {
		return nil
	}
	return &agentv1.Worktree{Path: w.Path, Branch: w.Branch, BaseRef: w.BaseRef, RepositoryPath: w.RepositoryPath}
}

// transcriptWorktree returns the worktree recorded in a transcript.
func transcriptWorktree(path string) *agentv1.Worktree {
	_, w := transcriptHead(path, false)
	return worktreeToProto(w)
}

// Bounds of transcriptHead.
const (
	headMaxLine  = 16 << 20
	headMaxBytes = 64 << 20
)

// transcriptHead reads a transcript for the session's working directory (the
// cwd of the first conversation entry) and its last recorded worktree. With
// stopAtCwd it stops at the first cwd: worktree metadata is written before
// the first message, so it has been seen by then (except for forks into a
// worktree, which recent folders also recognize by path).
func transcriptHead(path string, stopAtCwd bool) (string, *session.Worktree) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), headMaxLine)
	var cwd string
	var wt *session.Worktree
	read := 0
	for sc.Scan() && read < headMaxBytes {
		line := sc.Bytes()
		read += len(line) + 1
		var e struct {
			Type     string            `json:"type"`
			Cwd      string            `json:"cwd"`
			Worktree *session.Worktree `json:"worktree"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.Type == session.TypeMeta && e.Worktree != nil {
			wt = e.Worktree
		}
		if cwd == "" && e.Cwd != "" {
			cwd = e.Cwd
			if stopAtCwd {
				break
			}
		}
	}
	// A transcript outside its first folder's project directory was moved by SetSessionFolder (or forked from
	// another folder): its folder is the last recorded cwd, with the last recorded worktree.
	if cwd != "" && session.Slug(cwd) != filepath.Base(filepath.Dir(path)) {
		if last, lastWt := lastRecorded(path); last != "" {
			cwd = last
			if lastWt != nil {
				wt = lastWt
			}
		}
	}
	if wt != nil && wt.Path == "" {
		wt = nil // the session left its worktree
	}
	return cwd, wt
}

// lastRecorded returns the last cwd and the last worktree recorded in a transcript.
func lastRecorded(path string) (string, *session.Worktree) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), headMaxLine)
	var cwd string
	var wt *session.Worktree
	for sc.Scan() {
		var e struct {
			Type     string            `json:"type"`
			Cwd      string            `json:"cwd"`
			Worktree *session.Worktree `json:"worktree"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Cwd != "" {
			cwd = e.Cwd
		}
		if e.Type == session.TypeMeta && e.Worktree != nil {
			wt = e.Worktree
		}
	}
	return cwd, wt
}

// rfc3339 formats t in UTC.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }
