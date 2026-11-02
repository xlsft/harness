package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/worktree"
)

// createWorktree creates the worktree requested by CreateSession (nil when
// none was requested) and returns the session metadata to record.
func createWorktree(ctx context.Context, m *agentv1.CreateSessionRequest) (*worktree.Result, *session.Worktree, error) {
	o := m.GetWorktree()
	if o == nil {
		return nil, nil, nil
	}
	if m.GetCwd() == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cwd is required"))
	}
	r, err := worktree.Create(ctx, m.GetCwd(), worktree.Options{Name: o.GetName(), Branch: o.GetBranch(), BaseRef: o.GetBaseRef()})
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

// rollbackWorktree removes a worktree created for a session that failed to
// start; nothing has run in it yet.
func (s *Server) rollbackWorktree(r *worktree.Result) {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktree.AddTimeout)
	defer cancel()
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
	return cwd, wt
}

// rfc3339 formats t in UTC.
func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }
