package permissions

import (
	"context"
	"testing"
)

type askerFunc func(context.Context, Request) (Decision, error)

func (f askerFunc) Ask(ctx context.Context, r Request) (Decision, error) { return f(ctx, r) }

// A hook widens only what an ordinary prompt would have asked.
func TestHookBehavior(t *testing.T) {
	ctx := context.Background()
	asked := func(e *Engine) *bool {
		var n bool
		e.SetAsker(askerFunc(func(context.Context, Request) (Decision, error) {
			n = true
			return Decision{Behavior: Deny, Message: "no"}, nil
		}))
		return &n
	}
	hook := func(r Request, b Behavior) Request {
		r.HookBehavior, r.HookReason = b, "from a hook"
		return r
	}
	t.Run("allow passes a prompt", func(t *testing.T) {
		e := New(Config{Cwd: t.TempDir(), Home: t.TempDir()})
		was := asked(e)
		if d := e.Check(ctx, hook(bashReq("touch a", false), Allow)); d.Behavior != Allow || d.ResolvedBy != "hook" || *was {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("allow does not pass a deny rule", func(t *testing.T) {
		e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Deny: []string{"Bash(touch *)"}})
		if d := e.Check(ctx, hook(bashReq("touch a", false), Allow)); d.Behavior != Deny {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("allow does not pass plan mode", func(t *testing.T) {
		e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: ModePlan})
		if d := e.Check(ctx, hook(bashReq("touch a", false), Allow)); d.Behavior != Deny {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("allow does not answer what only the user may", func(t *testing.T) {
		for name, e := range map[string]*Engine{
			"ask rule":      New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Ask: []string{"Bash(touch *)"}}),
			"outside":       sandboxEngine(t, ModeDefault, SandboxState{Active: true}),
			"outside, auto": sandboxEngine(t, ModeAuto, SandboxState{Active: true}),
		} {
			was := asked(e)
			r := bashReq("touch a", name != "ask rule")
			if d := e.Check(ctx, hook(r, Allow)); d.Behavior != Deny || !*was {
				t.Fatalf("%s: %+v", name, d)
			}
		}
	})
	t.Run("ask prompts what would have run, whatever the mode", func(t *testing.T) {
		for _, mode := range []Mode{ModeDefault, ModeBypassPermissions, ModeAcceptEdits} {
			e := New(Config{Cwd: t.TempDir(), Home: t.TempDir(), DefaultMode: mode})
			was := asked(e)
			if d := e.Check(ctx, hook(bashReq("pwd", false), Ask)); d.Behavior != Deny || !*was {
				t.Fatalf("%s: %+v", mode, d)
			}
		}
	})
	t.Run("ask is refused when nobody can be asked", func(t *testing.T) {
		e := New(Config{Cwd: t.TempDir(), Home: t.TempDir()})
		if d := e.Check(ctx, hook(bashReq("pwd", false), Ask)); d.Behavior != Deny {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("the prompt hook answers ordinary prompts only", func(t *testing.T) {
		answer := func(context.Context, Request) (Decision, bool) {
			return Decision{Behavior: Allow, ResolvedBy: "hook"}, true
		}
		e := New(Config{Cwd: t.TempDir(), Home: t.TempDir()})
		was := asked(e)
		e.SetPromptHook(answer)
		if d := e.Check(ctx, bashReq("touch a", false)); d.Behavior != Allow || *was {
			t.Fatalf("%+v", d)
		}
		e = New(Config{Cwd: t.TempDir(), Home: t.TempDir(), Ask: []string{"Bash(touch *)"}})
		was = asked(e)
		e.SetPromptHook(answer)
		if d := e.Check(ctx, bashReq("touch a", false)); d.Behavior != Deny || !*was {
			t.Fatalf("an ask rule: %+v", d)
		}
	})
}
