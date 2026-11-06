package server

// The client's side of an editor extension (docs/design/ide.md): which edits go to the editor's diff and what its answer
// means, the mentions it adds to a draft, and IdeService.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	idev1 "github.com/xlsft/harness/packages/client/gen/harness/ide/v1"
	"github.com/xlsft/harness/packages/client/ide"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
	"github.com/xlsft/harness/packages/client/tools"
)

// maxRevertBytes is the largest baseline a review can restore.
const maxRevertBytes = 64 << 20

// editorAnswer is what came back from a diff in the editor.
type editorAnswer struct {
	outcome idev1.DiffOutcome
	comment string
	err     error
}

// editor is the editor this session reaches, or nil.
func (ls *liveSession) editor(ctx context.Context) *ide.Editor {
	if ls.sess.IDE == nil {
		return nil
	}
	return ls.sess.IDE.For(ctx, ls.sess.Dir())
}

// publishReview tells the frontends where a diff stands. It is not kept: a frontend that connects later learns nothing
// it could act on, since the question itself is in the PermissionRequest or in the editor.
func (ls *liveSession) publishReview(req permissions.Request, path, editor string, state agentv1.EditorReview_State) {
	ls.hub.publishEphemeral(&agentv1.SessionEvent{Event: &agentv1.SessionEvent_EditorReview{EditorReview: &agentv1.EditorReview{
		ToolUseId: req.ToolUseID, Path: path, State: state, Editor: editor,
	}}})
}

// openEditorDiff opens a call's proposed change in the editor. Nil channel when there is no diff to show. The returned
// function closes the diff if it is still open, and says so to the frontends.
func (ls *liveSession) openEditorDiff(ctx context.Context, req permissions.Request) (<-chan editorAnswer, func()) {
	if req.ToolName != "Edit" && req.ToolName != "Write" {
		return nil, func() {}
	}
	ed := ls.editor(ctx)
	if ed == nil {
		return nil, func() {}
	}
	p, err := tools.ProposeEdit(req.ToolName, req.Input)
	if err != nil {
		return nil, func() {}
	}
	diffCtx, cancel := context.WithCancel(ctx)
	ch := make(chan editorAnswer, 1)
	done := make(chan struct{})
	name := ed.Name()
	ls.publishReview(req, p.Path, name, agentv1.EditorReview_STATE_OPEN)
	go func() {
		defer close(done)
		resp, err := ed.ShowDiff(diffCtx, &idev1.ShowDiffRequest{
			DiffId: newUUID("diff_"), SessionId: ls.sess.ID, ToolUseId: req.ToolUseID, Path: p.Path, Exists: p.Exists,
			Before: p.Before, After: p.After, ToolName: req.ToolName,
		})
		if err != nil {
			ch <- editorAnswer{err: err}
			return
		}
		ch <- editorAnswer{outcome: resp.GetOutcome(), comment: resp.GetComment()}
	}()
	return ch, func() {
		cancel()
		<-done
		ls.publishReview(req, p.Path, name, agentv1.EditorReview_STATE_CLOSED)
	}
}

// answerFromEditor turns an answer into a decision; false when it answers nothing (closed, or the editor went away).
func answerFromEditor(a editorAnswer) (permissions.Decision, agentv1.RespondToPermissionRequest_Decision, bool) {
	if a.err != nil {
		return permissions.Decision{}, 0, false
	}
	switch a.outcome {
	case idev1.DiffOutcome_DIFF_OUTCOME_ACCEPTED:
		return permissions.Decision{Behavior: permissions.Allow, ResolvedBy: "user"}, agentv1.RespondToPermissionRequest_DECISION_ALLOW, true
	case idev1.DiffOutcome_DIFF_OUTCOME_REJECTED:
		// As in the panel: without a comment the turn stops; with one the model continues.
		return permissions.Decision{Behavior: permissions.Deny, Message: a.comment, Interrupt: a.comment == "", ResolvedBy: "user"}, agentv1.RespondToPermissionRequest_DECISION_DENY, true
	}
	return permissions.Decision{}, 0, false
}

// Review implements permissions.Reviewer: with the preference on, an edit the mode or a rule allows waits for the
// editor's diff. Closing that diff brings up the ordinary prompt: a turn never waits on nothing visible, and an edit the
// user wanted to review is not applied because a tab was closed. Without an editor the decision stands.
func (ls *liveSession) Review(ctx context.Context, req permissions.Request, d permissions.Decision) permissions.Decision {
	if ls.sess.IDE == nil || (req.ToolName != "Edit" && req.ToolName != "Write") || !ls.sess.IDEConfig().ReviewAllEdits {
		return d
	}
	ch, closeDiff := ls.openEditorDiff(ctx, req)
	if ch == nil {
		return d
	}
	defer closeDiff()
	ls.beginWait()
	defer ls.endWait()
	select {
	case <-ctx.Done():
		return permissions.Decision{Behavior: permissions.Deny, Message: "interrupted", ResolvedBy: "interrupt"}
	case a := <-ch:
		if a.err != nil {
			return d // the editor went away: as without one
		}
		if answer, _, ok := answerFromEditor(a); ok {
			if answer.Behavior == permissions.Allow {
				ls.noteAnswered(req)
			}
			return answer
		}
	}
	answer, err := ls.ask(ctx, req, false)
	if err != nil {
		return permissions.Decision{Behavior: permissions.Deny, Message: "interrupted", ResolvedBy: "interrupt"}
	}
	return answer
}

// noteAnswered remembers that a person answered for an edit of a file, so the editor need not show it again once it runs.
func (ls *liveSession) noteAnswered(req permissions.Request) {
	if req.ToolName != "Edit" && req.ToolName != "Write" {
		return
	}
	path := filepath.Clean(tools.FilePath(req.Input))
	ls.mu.Lock()
	if ls.answeredEdits == nil {
		ls.answeredEdits = map[string]bool{}
	}
	ls.answeredEdits[path] = true
	ls.mu.Unlock()
}

// takeAnswered says whether a person answered for this edit, once.
func (ls *liveSession) takeAnswered(path string) bool {
	path = filepath.Clean(path)
	ls.mu.Lock()
	defer ls.mu.Unlock()
	answered := ls.answeredEdits[path]
	delete(ls.answeredEdits, path)
	return answered
}

// ideEdits tells the editor about files the file tools wrote, after whatever else listens.
type ideEdits struct {
	tools.EditWatcher
	ls *liveSession
}

func (w ideEdits) FileEdited(ctx context.Context, env *tools.Env, path string) {
	w.EditWatcher.FileEdited(ctx, env, path)
	// Not on the tool's time: nothing depends on the answer.
	id, reviewed := w.ls.sess.ID, w.ls.takeAnswered(path)
	go func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if ed := w.ls.editor(bg); ed != nil {
			ed.EditApplied(bg, id, path, reviewed)
		}
	}()
}

// watchEditor connects a new session to its editor. The permissions engine and the tool environment are the session's,
// so this holds for as long as it does.
func (ls *liveSession) watchEditor() {
	if ls.sess.IDE == nil {
		return
	}
	ls.sess.Permissions.SetReviewer(ls)
	if env := ls.sess.ToolEnv; env != nil && env.Edits != nil {
		env.Edits = ideEdits{EditWatcher: env.Edits, ls: ls}
	}
}

// expandEditorMentions adds the lines of each @path#L10-20 in a message.
func (ls *liveSession) expandEditorMentions(ctx context.Context, content []llm.ContentBlock) []llm.ContentBlock {
	text := ""
	for _, b := range content {
		if b.Type == llm.BlockText {
			text = b.Text
			break
		}
	}
	if !strings.Contains(text, "#L") {
		return content
	}
	mentions := ide.ParseMentions(text, ls.sess.Dir())
	if len(mentions) == 0 {
		return content
	}
	ed := ls.editor(ctx)
	out := slices.Clone(content)
	for _, m := range mentions {
		if block := ide.ExpandMention(ctx, m, ed, ls.mayReadSilently); block != "" {
			out = append(out, llm.TextBlock(block))
		}
	}
	return out
}

// mayReadSilently: a mention adds text without a permission check, so only what the session could read inside its own
// folders. A relay viewer's message comes this way too.
func (ls *liveSession) mayReadSilently(path string) bool {
	env := ls.sess.ToolEnv
	if env == nil || (env.ReadDenied != nil && env.ReadDenied(path)) {
		return false
	}
	real := resolvedPath(path)
	for _, d := range env.WorkingDirs() {
		if _, ok := relWithinEither(resolvedPath(d), real); ok {
			return true
		}
	}
	return false
}

// ---- IdeService ----

type ideService struct{ s *Server }

func (s *Server) registerIde(mux *http.ServeMux) {
	path, h := agentv1connect.NewIdeServiceHandler(&ideService{s: s})
	mux.Handle(path, h)
}

func (i *ideService) GetEditorStatus(ctx context.Context, req *connect.Request[agentv1.GetEditorStatusRequest]) (*connect.Response[agentv1.GetEditorStatusResponse], error) {
	ls, err := i.s.get(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	resp := &agentv1.GetEditorStatusResponse{}
	if ed := ls.editor(ctx); ed != nil {
		resp.Attached, resp.Name = true, ed.Name()
	}
	return connect.NewResponse(resp), nil
}

// fileBase is a changed file's baseline: what the review compares with and what Reject restores.
type fileBase struct {
	exists bool
	data   []byte
	mode   os.FileMode
	// refused says why it cannot be restored.
	refused string
}

// sessionFileBase finds path among what the session changed, against HEAD in a repository and the snapshot elsewhere.
func sessionFileBase(ctx context.Context, src *diffSources, path string) (*fileBase, error) {
	b, err := scanSessionDiff(ctx, src, &agentv1.DiffBase{Kind: agentv1.DiffBaseKind_DIFF_BASE_KIND_UNCOMMITTED})
	if err != nil {
		return nil, err
	}
	for _, r := range b.repos {
		for _, e := range r.entries {
			if r.abs(e.path) != path {
				continue
			}
			if e.oldPath != "" {
				return &fileBase{refused: "it was renamed; undo the rename with git"}, nil
			}
			if strings.Trim(e.oldOID, "0") == "" {
				return &fileBase{}, nil // added, also when staged
			}
			return repoFileBase(ctx, r, e.path)
		}
		for _, p := range r.untracked {
			if r.abs(p) == path {
				return &fileBase{}, nil
			}
		}
	}
	for _, c := range b.candidates {
		if sanitizeUTF8(c.path) != path {
			continue
		}
		if c.baseline == "" {
			if c.noCopy {
				return &fileBase{refused: "the session kept no copy of it"}, nil
			}
			return &fileBase{}, nil
		}
		st, err := os.Stat(c.baseline)
		if err != nil {
			return &fileBase{refused: "its snapshot is gone"}, nil
		}
		if st.Size() > maxRevertBytes {
			return &fileBase{refused: "it is too large"}, nil
		}
		data, err := os.ReadFile(c.baseline)
		if err != nil {
			return nil, err
		}
		return &fileBase{exists: true, data: data, mode: st.Mode().Perm()}, nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s has no changes", path))
}

func repoFileBase(ctx context.Context, r *diffRepo, rel string) (*fileBase, error) {
	if len(r.revs) == 0 {
		return &fileBase{refused: "the repository has no base to compare with"}, nil
	}
	tree, err := diffGit(ctx, r.top, "", "ls-tree", "-z", r.revs[0], "--", ":(top,literal)"+rel)
	if err != nil {
		return nil, err
	}
	mode, _, _ := strings.Cut(string(tree), " ")
	switch mode {
	case "100644":
		mode = "644"
	case "100755":
		mode = "755"
	default:
		return &fileBase{refused: "it is not a regular file in git"}, nil
	}
	data, err := diffGit(ctx, r.top, "", "cat-file", "blob", r.revs[0]+":"+rel)
	if err != nil {
		return nil, err
	}
	if len(data) > maxRevertBytes {
		return &fileBase{refused: "it is too large"}, nil
	}
	perm := os.FileMode(0o644)
	if mode == "755" {
		perm = 0o755
	}
	return &fileBase{exists: true, data: data, mode: perm}, nil
}

func (i *ideService) GetSessionFileBase(ctx context.Context, req *connect.Request[agentv1.GetSessionFileBaseRequest]) (*connect.Response[agentv1.GetSessionFileBaseResponse], error) {
	src, err := i.s.diffSourcesOf(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	base, err := sessionFileBase(ctx, src, req.Msg.GetPath())
	if err != nil {
		return nil, diffRPCError(ctx, err)
	}
	if base.refused != "" || !utf8.Valid(base.data) {
		return connect.NewResponse(&agentv1.GetSessionFileBaseResponse{Exists: base.exists || base.refused != "", Binary: true}), nil
	}
	return connect.NewResponse(&agentv1.GetSessionFileBaseResponse{Exists: base.exists, Content: string(base.data)}), nil
}

func (i *ideService) RevertSessionFile(ctx context.Context, req *connect.Request[agentv1.RevertSessionFileRequest]) (*connect.Response[agentv1.RevertSessionFileResponse], error) {
	src, err := i.s.diffSourcesOf(req.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	path := req.Msg.GetPath()
	base, err := sessionFileBase(ctx, src, path)
	if err != nil {
		return nil, diffRPCError(ctx, err)
	}
	if base.refused != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s can't be restored: %s", path, base.refused))
	}
	st, statErr := os.Lstat(path)
	if statErr == nil && !st.Mode().IsRegular() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s can't be restored: it is not a regular file now", path))
	}
	if !base.exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&agentv1.RevertSessionFileResponse{Removed: true}), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := os.WriteFile(path, base.data, base.mode); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.RevertSessionFileResponse{}), nil
}
