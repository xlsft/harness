package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xlsft/harness/packages/client/session"
	"github.com/xlsft/harness/packages/client/worktree"
)

// The tools that move a session while it works: into a new worktree and out again, to another allowed folder, onto
// another branch, and up to date with a worktree's base. Git work happens here (package worktree); the session state
// that follows (working directories, permissions, transcript, sync, what the frontends show) belongs to the Mover.
//
// Only the user grants access: ChangeDirectory goes where the session may already work, and RequestDirectory adds a
// folder only when the user picks Allow on its card.

// Move is one change of the session's primary folder.
type Move struct {
	Cwd string
	// Worktree is the worktree Cwd is in (nil: none; leaving one clears it).
	Worktree *session.Worktree
	// KeepPrevious keeps the working directories the session had as working directories, for moving back.
	KeepPrevious bool
	// Notice says in a sentence what happened, for the transcript.
	Notice string
}

// Mover is the session side of these tools. Move and AllowDirectory can run while a turn runs: that is when the model
// calls them.
type Mover interface {
	// Worktree returns the session's worktree (a copy; nil when it has none).
	Worktree() *session.Worktree
	// Move switches the running session to another folder. On an error the session stays where it was.
	Move(ctx context.Context, m Move) error
	// AllowDirectory makes dir a working directory, on the user's answer.
	AllowDirectory(dir string) error
	// SetBranch records that the session's folder is on branch now and, when not empty, that its worktree's base is
	// baseRef.
	SetBranch(ctx context.Context, branch, baseRef, notice string)
	// OthersIn returns the ids of other open sessions working inside path.
	OthersIn(path string) []string
}

func moverOf(env *Env) (Mover, Result, bool) {
	m := env.Mover()
	if m == nil {
		return nil, ErrorResult("this session cannot change its folder"), false
	}
	return m, Result{}, true
}

// inside reports whether path is dir or below it.
func inside(path, dir string) bool { return within(path, []string{dir}) }

// moveResult builds the result of a move: the first line for the model, and the row summary.
func moveResult(text, summary string, structured map[string]any) Result {
	r := TextResult(text, structured)
	r.Summary = summary
	return r
}

// ---- EnterWorktree ----

// EnterWorktree creates a worktree of the current repository and moves the session into it.
type EnterWorktree struct{}

func (*EnterWorktree) Name() string                  { return "EnterWorktree" }
func (*EnterWorktree) ReadOnly(json.RawMessage) bool { return false }

func (*EnterWorktree) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Name    string `json:"name"`
		BaseRef string `json:"base_ref"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	m, bad, ok := moverOf(env)
	if !ok {
		return bad
	}
	if w := m.Worktree(); w != nil {
		return ErrorResult("the session already runs in the worktree %s (branch %s). Leave it with ExitWorktree first.", w.Path, w.Branch)
	}
	cwd := env.CurrentDir()
	created, err := worktree.Create(ctx, cwd, worktree.Options{Name: in.Name, BaseRef: in.BaseRef})
	if errors.Is(err, worktree.ErrNotRepository) {
		return ErrorResult("%s is not inside a git repository, so there is nothing to make a worktree of.", cwd)
	}
	if err != nil {
		return ErrorResult("could not create the worktree: %v", err)
	}
	wt := &session.Worktree{Path: created.Path, Branch: created.Branch, BaseRef: created.BaseRef, RepositoryPath: created.RepositoryPath, OriginalCwd: cwd}
	notice := fmt.Sprintf("Moved to the worktree %s, on the new branch %s from %s.", created.Path, created.Branch, created.BaseRef)
	if err := m.Move(ctx, Move{Cwd: created.Cwd, Worktree: wt, Notice: notice}); err != nil {
		if rerr := worktree.Rollback(context.WithoutCancel(ctx), created); rerr != nil {
			return ErrorResult("could not move the session into the worktree (%v), and %s could not be removed: %v", err, created.Path, rerr)
		}
		return ErrorResult("could not move the session into the worktree: %v", err)
	}
	text := fmt.Sprintf("The session now works in %s, on branch %s (from %s). Your shell is there. Use paths under the new folder, and read files again before editing them there.", created.Cwd, created.Branch, created.BaseRef)
	if dirty, err := worktree.Dirty(ctx, cwd); err == nil && len(dirty) > 0 {
		text += fmt.Sprintf(" %d uncommitted change(s) stayed behind in %s: a worktree starts from a commit.", len(dirty), cwd)
	}
	return moveResult(text, "Entered "+created.Branch, map[string]any{"cwd": created.Cwd, "worktreePath": created.Path, "branch": created.Branch, "baseRef": created.BaseRef})
}

// ---- ExitWorktree ----

// ExitWorktree moves the session back to where it came from, keeping or removing the worktree.
type ExitWorktree struct{}

func (*ExitWorktree) Name() string                  { return "ExitWorktree" }
func (*ExitWorktree) ReadOnly(json.RawMessage) bool { return false }

type exitWorktreeInput struct {
	Action         string `json:"action"`
	DiscardChanges bool   `json:"discard_changes"`
}

// PermissionHints implements PermissionHinter: throwing away uncommitted work needs the user, in every mode.
func (*ExitWorktree) PermissionHints(ctx context.Context, env *Env, call Call) PermissionHints {
	var in exitWorktreeInput
	m := env.Mover()
	if m == nil || decode(call.Input, &in) != nil || in.Action != "remove" || !in.DiscardChanges {
		return PermissionHints{}
	}
	if w := m.Worktree(); w != nil {
		if dirty, err := worktree.Dirty(ctx, w.Path); err != nil || len(dirty) > 0 {
			return PermissionHints{Sensitive: fmt.Sprintf("Removes the worktree %s and discards its uncommitted changes (%d files).", w.Path, len(dirty))}
		}
	}
	return PermissionHints{}
}

func (*ExitWorktree) Run(ctx context.Context, env *Env, call Call) Result {
	var in exitWorktreeInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.Action != "keep" && in.Action != "remove" {
		return ErrorResult("InputValidationError: action must be keep or remove")
	}
	m, bad, ok := moverOf(env)
	if !ok {
		return bad
	}
	w := m.Worktree()
	if w == nil {
		return ErrorResult("the session does not run in a worktree.")
	}
	remove := in.Action == "remove"
	ahead, aheadErr := 0, error(nil)
	force := false
	if remove {
		dirty, err := worktree.Dirty(ctx, w.Path)
		if err != nil {
			return ErrorResult("could not read the state of %s: %v", w.Path, err)
		}
		if len(dirty) > 0 && !in.DiscardChanges {
			return ErrorResult("%s has %d uncommitted change(s): %s. Commit them, leave with action keep, or, only if they are meant to be thrown away, pass discard_changes: true.", w.Path, len(dirty), firstFiles(dirty))
		}
		force = len(dirty) > 0
		if env.Tasks != nil && env.Tasks.Running() > 0 {
			return ErrorResult("%d background task(s) of this session are running and may be using the worktree. Stop them with TaskStop first, or leave with action keep.", env.Tasks.Running())
		}
		if others := m.OthersIn(w.Path); len(others) > 0 {
			return ErrorResult("another open session works in %s (%s), so it cannot be removed. Leave with action keep.", w.Path, strings.Join(others, ", "))
		}
		ahead, aheadErr = worktree.CommitsAhead(ctx, w.Path, w.BaseRef)
	}
	target := exitTarget(ctx, env.CurrentDir(), w)
	notice := fmt.Sprintf("Moved back to %s; the worktree %s stays.", target, w.Path)
	if remove {
		notice = fmt.Sprintf("Moved back to %s and removed the worktree %s.", target, w.Path)
	}
	if err := m.Move(ctx, Move{Cwd: target, Notice: notice}); err != nil {
		return ErrorResult("could not move the session back to %s: %v", target, err)
	}
	structured := map[string]any{"cwd": target, "worktreePath": w.Path, "branch": w.Branch, "action": in.Action}
	if !remove {
		return moveResult(fmt.Sprintf("The session now works in %s. The worktree %s (branch %s) stays as it is.", target, w.Path, w.Branch), "Left the worktree", structured)
	}
	// The session has left: from here on a failure leaves a worktree nobody works in, which is only reported.
	r := worktree.Removal{RepositoryPath: w.RepositoryPath, Path: w.Path, Force: force}
	// Only a branch the client named for this worktree, and only with nothing on it that base lacks.
	keptBranch := true
	if aheadErr == nil && ahead == 0 && strings.HasPrefix(w.Branch, worktree.BranchPrefix) {
		r.Branch, keptBranch = w.Branch, false
	}
	if err := worktree.Remove(context.WithoutCancel(ctx), r); err != nil {
		structured["removeError"] = err.Error()
		return moveResult(fmt.Sprintf("The session now works in %s, but removing the worktree failed: %v", target, err), "Left the worktree", structured)
	}
	text := fmt.Sprintf("The session now works in %s. The worktree %s is gone.", target, w.Path)
	if keptBranch {
		text += fmt.Sprintf(" Its branch %s stays", w.Branch)
		if ahead > 0 {
			text += fmt.Sprintf(", with %d commit(s) that %s does not have", ahead, w.BaseRef)
		}
		text += "."
	}
	structured["branchKept"] = keptBranch
	return moveResult(text, "Removed the worktree", structured)
}

// exitTarget is where leaving a worktree goes: the folder the session came from, else the same subdirectory of the
// main checkout, else the checkout itself.
func exitTarget(ctx context.Context, cwd string, w *session.Worktree) string {
	isDir := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	}
	if w.OriginalCwd != "" && isDir(w.OriginalCwd) {
		return w.OriginalCwd
	}
	if rel, err := filepath.Rel(w.Path, cwd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if p := filepath.Join(w.RepositoryPath, rel); isDir(p) {
			return p
		}
	}
	return w.RepositoryPath
}

func firstFiles(files []string) string {
	const show = 10
	if len(files) <= show {
		return strings.Join(files, ", ")
	}
	return strings.Join(files[:show], ", ") + fmt.Sprintf(" and %d more", len(files)-show)
}

// ---- ChangeDirectory ----

// ChangeDirectory moves the session to a folder it may already work in.
type ChangeDirectory struct{}

func (*ChangeDirectory) Name() string { return "ChangeDirectory" }

// ReadOnly: the call changes no file, and it cannot widen what the session may touch.
func (*ChangeDirectory) ReadOnly(json.RawMessage) bool { return true }

func (*ChangeDirectory) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return ErrorResult("InputValidationError: path is required")
	}
	m, bad, ok := moverOf(env)
	if !ok {
		return bad
	}
	target, res, ok := existingFolder(env, in.Path)
	if !ok {
		return res
	}
	if !within(target, env.WorkingDirs()) {
		return ErrorResult("%s is not a folder this session may work in. Ask for it with RequestDirectory.", target)
	}
	return moveThere(ctx, env, m, target)
}

// existingFolder resolves a path argument to an existing directory.
func existingFolder(env *Env, p string) (string, Result, bool) {
	target := resolvePath(env.CurrentDir(), strings.TrimSpace(p))
	st, err := os.Stat(target)
	if err != nil {
		return "", ErrorResult("%s does not exist.", target), false
	}
	if !st.IsDir() {
		return "", ErrorResult("%s is not a folder.", target), false
	}
	return target, Result{}, true
}

// moveThere moves the session to target, a folder it may work in.
func moveThere(ctx context.Context, env *Env, m Mover, target string) Result {
	cwd := env.CurrentDir()
	if evalPath(target) == evalPath(cwd) {
		return moveResult("The session already works in "+cwd+".", "Already there", map[string]any{"cwd": cwd})
	}
	w := m.Worktree()
	if w != nil && !inside(target, w.Path) {
		return ErrorResult("the session runs in the worktree %s. Leave it with ExitWorktree before moving elsewhere.", w.Path)
	}
	notice := fmt.Sprintf("Moved to %s.", target)
	if err := m.Move(ctx, Move{Cwd: target, Worktree: w, KeepPrevious: true, Notice: notice}); err != nil {
		return ErrorResult("could not move the session to %s: %v", target, err)
	}
	text := fmt.Sprintf("The session now works in %s. Your shell is there, and %s stays allowed.", target, cwd)
	return moveResult(text, "Moved to "+filepath.Base(target), map[string]any{"cwd": target, "previous": cwd})
}

// ---- RequestDirectory ----

// Labels of the RequestDirectory card.
const (
	directoryAllow = "Allow"
	directoryDeny  = "Deny"
)

// RequestDirectory asks the user to let the session work in a folder; only their answer grants it.
type RequestDirectory struct{}

func (*RequestDirectory) Name() string { return "RequestDirectory" }

// ReadOnly: nothing happens without the user's answer on the card, so no permission prompt comes first.
func (*RequestDirectory) ReadOnly(json.RawMessage) bool { return true }

// oneChoice asks one question with fixed options on the question card and returns the raw response.
func oneChoice(ctx context.Context, env *Env, call Call, header, question string, other bool, options ...QuestionOption) (QuestionResponse, error) {
	h := env.Host()
	if h == nil || h.Interactor() == nil {
		return QuestionResponse{}, ErrNonInteractive
	}
	q := Question{Question: question, Header: header, Type: QuestionSingle, Options: options, AllowOther: &other}
	return h.Interactor().AskQuestions(ctx, call.ID, []Question{q})
}

func (*RequestDirectory) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
		Move   bool   `json:"move"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return ErrorResult("InputValidationError: path is required")
	}
	m, bad, ok := moverOf(env)
	if !ok {
		return bad
	}
	target, res, ok := existingFolder(env, in.Path)
	if !ok {
		return res
	}
	if w := m.Worktree(); in.Move && w != nil && !inside(target, w.Path) {
		return ErrorResult("the session runs in the worktree %s. Leave it with ExitWorktree before moving elsewhere, or ask without move.", w.Path)
	}
	if within(target, env.WorkingDirs()) {
		if in.Move {
			return moveThere(ctx, env, m, target)
		}
		return moveResult(target+" is already a working directory of this session.", "Already allowed", map[string]any{"path": target, "allowed": true})
	}
	question := fmt.Sprintf("Allow this session to work in %s?", target)
	if in.Move {
		question = fmt.Sprintf("Allow this session to work in %s and move there?", target)
	}
	if r := strings.TrimSpace(in.Reason); r != "" {
		question += "\n" + r
	}
	resp, err := oneChoice(ctx, env, call, "Folder", question, false,
		QuestionOption{Label: directoryAllow, Description: "It can read and edit files there, for this session"},
		QuestionOption{Label: directoryDeny, Description: "The session stays where it is"})
	if errors.Is(err, ErrNonInteractive) {
		return ErrorResult("no user is attached to allow %s. Say in your answer what you need.", target)
	}
	if err != nil {
		return ErrorResult("the question was not answered: %v", err)
	}
	if !answeredWith(resp, directoryAllow) {
		text := fmt.Sprintf("The user did not allow %s. Go on without it, or tell them what you could not do.", target)
		if resp.Outcome == QuestionMessage {
			text = fmt.Sprintf("The user did not allow %s and wrote instead: %s", target, resp.MessageText())
		}
		r := TextResult(text, map[string]any{"path": target, "allowed": false})
		r.IsError, r.Summary = true, "Denied"
		return r
	}
	if err := m.AllowDirectory(target); err != nil {
		return ErrorResult("could not add %s: %v", target, err)
	}
	if in.Move {
		return moveThere(ctx, env, m, target)
	}
	return moveResult(fmt.Sprintf("The user allowed %s: it is a working directory of this session now. The session still works in %s.", target, env.CurrentDir()),
		"Allowed "+filepath.Base(target), map[string]any{"path": target, "allowed": true})
}

// answeredWith reports whether the user picked exactly this option of a one-question card.
func answeredWith(r QuestionResponse, label string) bool {
	return r.Outcome == QuestionAnswered && len(r.Answers) == 1 && len(r.Answers[0].Selected) == 1 && r.Answers[0].Selected[0] == label
}

// ---- ProposeBranch ----

const (
	branchAccept  = "Accept"
	branchDecline = "Not now"
)

// ProposeBranch suggests a branch for the user to accept, then creates it or switches to it.
type ProposeBranch struct{}

func (*ProposeBranch) Name() string { return "ProposeBranch" }

// ReadOnly: nothing happens without the user's answer on the card, so no permission prompt comes first.
func (*ProposeBranch) ReadOnly(json.RawMessage) bool { return true }

func (*ProposeBranch) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return ErrorResult("InputValidationError: name is required")
	}
	m, bad, ok := moverOf(env)
	if !ok {
		return bad
	}
	cwd := env.CurrentDir()
	if _, err := worktree.Find(ctx, cwd); err != nil {
		return ErrorResult("%s is not inside a git repository.", cwd)
	}
	if err := worktree.CheckBranchName(ctx, cwd, name); err != nil {
		return ErrorResult("%q cannot be a branch name.", name)
	}
	if worktree.CurrentBranch(ctx, cwd) == name {
		return moveResult("The session is already on branch "+name+".", "Already on "+name, map[string]any{"branch": name})
	}
	verb := "Create the branch %s and switch to it?"
	if worktree.BranchExists(ctx, cwd, name) {
		verb = "Switch to the branch %s?"
	}
	question := fmt.Sprintf(verb, name)
	if r := strings.TrimSpace(in.Reason); r != "" {
		question += "\n" + r
	}
	resp, err := oneChoice(ctx, env, call, "Branch", question, true,
		QuestionOption{Label: branchAccept, Recommended: true},
		QuestionOption{Label: branchDecline})
	if errors.Is(err, ErrNonInteractive) {
		return ErrorResult("no user is attached to accept the branch %s. Say in your answer which branch you suggest.", name)
	}
	if err != nil {
		return ErrorResult("the question was not answered: %v", err)
	}
	chosen := ""
	switch {
	case answeredWith(resp, branchAccept):
		chosen = name
	case resp.Outcome == QuestionAnswered && len(resp.Answers) == 1 && strings.TrimSpace(resp.Answers[0].Text) != "":
		chosen = strings.TrimSpace(resp.Answers[0].Text) // the user typed another name
	default:
		text := fmt.Sprintf("The user did not accept the branch %s; the session stays on its branch.", name)
		if resp.Outcome == QuestionMessage {
			text = fmt.Sprintf("The user did not accept the branch %s and wrote instead: %s", name, resp.MessageText())
		}
		r := TextResult(text, map[string]any{"branch": name, "accepted": false})
		r.Summary = "Not accepted"
		return r
	}
	created, err := worktree.SwitchBranch(ctx, cwd, chosen)
	if err != nil {
		return ErrorResult("could not switch to %s: %v", chosen, err)
	}
	notice := "Switched to the branch " + chosen + "."
	if created {
		notice = "Created the branch " + chosen + " and switched to it."
	}
	m.SetBranch(ctx, chosen, "", notice)
	text := "The session is on the existing branch " + chosen + " now."
	if created {
		text = "The session is on the new branch " + chosen + " now."
	}
	return moveResult(text, "On "+chosen, map[string]any{"branch": chosen, "accepted": true, "created": created})
}

// ---- SyncWithBase ----

// SyncWithBase merges a worktree's base into its branch.
type SyncWithBase struct{}

func (*SyncWithBase) Name() string                  { return "SyncWithBase" }
func (*SyncWithBase) ReadOnly(json.RawMessage) bool { return false }

func (*SyncWithBase) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		BaseRef string `json:"base_ref"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	m, bad, ok := moverOf(env)
	if !ok {
		return bad
	}
	w := m.Worktree()
	if w == nil {
		return ErrorResult("the session does not run in a worktree, so it has no base to sync with.")
	}
	base := strings.TrimSpace(in.BaseRef)
	if base == "" {
		base = w.BaseRef
	}
	if base == "" {
		return ErrorResult("the worktree has no base on record. Name one with base_ref.")
	}
	res, err := worktree.Sync(ctx, w.Path, base)
	switch {
	case errors.Is(err, worktree.ErrMergeInProgress):
		return ErrorResult("a merge is in progress in %s. Resolve its conflicts, then `git add` the files and `git commit`, or back out with `git merge --abort`.", w.Path)
	case errors.Is(err, worktree.ErrUncommitted):
		return ErrorResult("%v. Commit them first, then sync.", err)
	case err != nil:
		return ErrorResult("could not sync with %s: %v", base, err)
	}
	structured := map[string]any{"status": res.Status, "target": res.Target, "commits": res.Commits}
	note := ""
	if res.FetchError != "" {
		structured["fetchError"] = res.FetchError
		note = " The fetch failed (" + res.FetchError + "), so this used what the repository already had."
	}
	baseRef := ""
	if res.Target != w.BaseRef && !worktree.IsCommit(res.Target) {
		baseRef = res.Target // the Changes pane compares with what the branch was brought up to
	}
	branch := worktree.CurrentBranch(ctx, w.Path)
	switch res.Status {
	case worktree.SyncUpToDate:
		if baseRef != "" {
			m.SetBranch(ctx, branch, baseRef, "")
		}
		return moveResult(fmt.Sprintf("Branch %s already has everything in %s.%s", w.Branch, res.Target, note), "Up to date", structured)
	case worktree.SyncConflicts:
		structured["conflicts"] = res.Conflicts
		m.SetBranch(ctx, branch, baseRef, fmt.Sprintf("Merging %s into %s stopped at %d conflicting file(s).", res.Target, w.Branch, len(res.Conflicts)))
		r := moveResult(fmt.Sprintf("Merging %s into %s stopped at conflicts in these files:\n%s\nThe merge is in progress: fix them, `git add` them and `git commit`, or back out with `git merge --abort`.%s",
			res.Target, w.Branch, strings.Join(res.Conflicts, "\n"), note), fmt.Sprintf("%d conflicts", len(res.Conflicts)), structured)
		return r
	}
	m.SetBranch(ctx, branch, baseRef, fmt.Sprintf("Merged %d commit(s) from %s into %s.", res.Commits, res.Target, w.Branch))
	return moveResult(fmt.Sprintf("Merged %d commit(s) from %s into %s.%s", res.Commits, res.Target, w.Branch, note), fmt.Sprintf("Merged %d commits", res.Commits), structured)
}
