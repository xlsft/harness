package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// The tools of a project run's coordinator session (docs/design/coordinator.md). They are offered only to a session
// that coordinates a run; the run, its limits and its workers live in the agent service (server/projectruns.go), which
// the tools reach through the session's Interactor (ProjectCoordinator).

// ProjectWorkerRequest is a worker to start for a task of the coordinator's graph.
type ProjectWorkerRequest struct {
	TaskID, Prompt string
	// Target is "worktree" (default), "here" or a host id from ProjectStatus; Cwd is the folder on that host.
	Target, Cwd string
	Model       string
}

// ProjectCoordinator is implemented by an Interactor whose session coordinates a project run.
type ProjectCoordinator interface {
	ProjectStatus(ctx context.Context) (string, error)
	ProjectWorkerStart(ctx context.Context, r ProjectWorkerRequest) (string, error)
	ProjectWorkerMerge(ctx context.Context, worker string) (string, error)
	ProjectFinish(ctx context.Context, ok bool, summary string) (string, error)
}

const noProjectRun = "This session coordinates no project run."

func projectCoordinator(env *Env) ProjectCoordinator {
	h := env.Host()
	if h == nil {
		return nil
	}
	c, _ := h.Interactor().(ProjectCoordinator)
	return c
}

// RegisterProjectTools adds the coordinator's tools to r; on reports whether the session coordinates a run (the tools
// are hidden from the model otherwise).
func RegisterProjectTools(r *Registry, on func() bool) {
	for _, t := range []Tool{&ProjectStatus{on: on}, &ProjectWorkerStart{on: on}, &ProjectWorkerMerge{on: on}, &ProjectFinish{on: on}} {
		r.Register(t)
	}
}

type projectTool struct{ on func() bool }

func (t projectTool) HiddenFromModel() bool { return t.on == nil || !t.on() }

func runProject(ctx context.Context, env *Env, call Call, t projectTool, input any, fn func(ProjectCoordinator) (string, error)) Result {
	c := projectCoordinator(env)
	if c == nil || t.HiddenFromModel() {
		return ErrorResult("%s", noProjectRun)
	}
	if input != nil && len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, input); err != nil {
			return ErrorResult("invalid input: %v", err)
		}
	}
	out, err := fn(c)
	if err != nil {
		return ErrorResult("%s", err.Error())
	}
	return TextResult(out, nil)
}

// ProjectStatus shows the run: workers, budget and where workers can run.
type ProjectStatus struct{ on func() bool }

func (*ProjectStatus) Name() string                  { return "ProjectStatus" }
func (*ProjectStatus) ReadOnly(json.RawMessage) bool { return true }
func (t *ProjectStatus) HiddenFromModel() bool       { return projectTool{t.on}.HiddenFromModel() }
func (t *ProjectStatus) Run(ctx context.Context, env *Env, call Call) Result {
	return runProject(ctx, env, call, projectTool{t.on}, nil, func(c ProjectCoordinator) (string, error) { return c.ProjectStatus(ctx) })
}

// ProjectWorkerStart starts a worker session for a task of the graph. Workers get the coordinator's permission mode and
// rules, never more; the run's limits are checked by the agent service, not here.
type ProjectWorkerStart struct{ on func() bool }

func (*ProjectWorkerStart) Name() string                  { return "ProjectWorkerStart" }
func (*ProjectWorkerStart) ReadOnly(json.RawMessage) bool { return true }
func (t *ProjectWorkerStart) HiddenFromModel() bool       { return projectTool{t.on}.HiddenFromModel() }
func (t *ProjectWorkerStart) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		TaskID string `json:"task_id"`
		Prompt string `json:"prompt"`
		Target string `json:"target"`
		Cwd    string `json:"cwd"`
		Model  string `json:"model"`
	}
	return runProject(ctx, env, call, projectTool{t.on}, &in, func(c ProjectCoordinator) (string, error) {
		if strings.TrimSpace(in.TaskID) == "" || strings.TrimSpace(in.Prompt) == "" {
			return "", errors.New("task_id and prompt are required")
		}
		return c.ProjectWorkerStart(ctx, ProjectWorkerRequest{TaskID: in.TaskID, Prompt: in.Prompt, Target: in.Target, Cwd: in.Cwd, Model: in.Model})
	})
}

// ProjectWorkerMerge merges a finished worker's worktree branch into the coordinator's branch. It changes the repository,
// so it goes through the permission check like a command.
type ProjectWorkerMerge struct{ on func() bool }

func (*ProjectWorkerMerge) Name() string                  { return "ProjectWorkerMerge" }
func (*ProjectWorkerMerge) ReadOnly(json.RawMessage) bool { return false }
func (t *ProjectWorkerMerge) HiddenFromModel() bool       { return projectTool{t.on}.HiddenFromModel() }
func (t *ProjectWorkerMerge) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Worker string `json:"worker"`
	}
	return runProject(ctx, env, call, projectTool{t.on}, &in, func(c ProjectCoordinator) (string, error) {
		if strings.TrimSpace(in.Worker) == "" {
			return "", errors.New("worker is required: its name, session id or task id")
		}
		return c.ProjectWorkerMerge(ctx, in.Worker)
	})
}

// ProjectFinish ends the run with a summary.
type ProjectFinish struct{ on func() bool }

func (*ProjectFinish) Name() string                  { return "ProjectFinish" }
func (*ProjectFinish) ReadOnly(json.RawMessage) bool { return true }
func (t *ProjectFinish) HiddenFromModel() bool       { return projectTool{t.on}.HiddenFromModel() }
func (t *ProjectFinish) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Outcome string `json:"outcome"`
		Summary string `json:"summary"`
	}
	return runProject(ctx, env, call, projectTool{t.on}, &in, func(c ProjectCoordinator) (string, error) {
		if strings.TrimSpace(in.Summary) == "" {
			return "", errors.New("summary is required")
		}
		return c.ProjectFinish(ctx, in.Outcome != "failed", in.Summary)
	})
}
