package tools

import (
	"context"
	"encoding/json"
	"strings"
)

// SkillInfo is what the Skill tool's permission check needs to know of a skill.
type SkillInfo struct {
	// Runnable: the model may run it. False for an unknown name too; the call can then only report an error.
	Runnable bool
	// Harmless: it only adds text to the conversation. It grants no tools and starts no agent.
	Harmless bool
}

// SkillRunner runs skills for the Skill tool (the session's wiring in bootstrap).
type SkillRunner interface {
	SkillInfo(name string) SkillInfo
	// RunSkill starts a skill for the agent env belongs to: its text reaches the model with the next request.
	RunSkill(ctx context.Context, env *Env, toolUseID, name, args string) Result
}

// Skill loads a skill's full text into the conversation, on the model's own decision. The listing the model has holds
// names and descriptions only.
type Skill struct{}

// Name implements Tool.
func (*Skill) Name() string { return "Skill" }

// ReadOnly implements Tool. Whether the call is harmless depends on the skill: see PermissionHints.
func (*Skill) ReadOnly(json.RawMessage) bool { return false }

type skillInput struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

func (in skillInput) name() string { return strings.TrimPrefix(strings.TrimSpace(in.Skill), "/") }

// PermissionHints implements PermissionHinter. Text alone is no more than reading a file; a skill that grants tools or
// forks an agent changes what can happen next, so the usual check applies, with the rules on Skill(name).
func (*Skill) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	var in skillInput
	if env.Skills == nil || decode(call.Input, &in) != nil {
		return PermissionHints{NoEffect: true}
	}
	info := env.Skills.SkillInfo(in.name())
	if !info.Runnable {
		return PermissionHints{NoEffect: true}
	}
	return PermissionHints{ReadOnly: info.Harmless, Label: "Skill " + in.name()}
}

// Run implements Tool.
func (*Skill) Run(ctx context.Context, env *Env, call Call) Result {
	var in skillInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.name() == "" {
		return ErrorResult("InputValidationError: skill is required")
	}
	if env.Skills == nil {
		return ErrorResult("Skills are not available in this session.")
	}
	return env.Skills.RunSkill(ctx, env, call.ID, in.name(), in.Args)
}
