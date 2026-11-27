package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/tools"
	"github.com/xlsft/harness/packages/client/toolsearch"
)

// Tool deferral (docs/design/tool-deferral.md). The request's tool list is, in this order:
//
//  1. the bundle definitions of the registered tools that are not deferred, in registration order;
//  2. ExtraTools (MCP tools that are not deferred, browser fallbacks);
//  3. the deferred tools the session loaded, built-in and MCP alike, in load order.
//
// The first two parts are fixed for the session; a load appends to the third. Tools sit at the very start of the
// cached prefix, so a load still costs one cache write of the prompt that follows it; it never reorders what was
// already there.

// canSearch reports whether this agent defers tools: it has a loader and can call ToolSearch. An agent whose tool
// list leaves ToolSearch out (a read-only subagent type) gets its deferred tools in full instead.
func (a *Agent) canSearch() bool {
	if a.cfg.Deferral == nil || a.cfg.DisabledTools[toolsearch.Name] {
		return false
	}
	_, ok := a.cfg.Tools.Get(toolsearch.Name)
	return ok
}

// offerable reports whether a registered tool may be offered to the model at all.
func (a *Agent) offerable(n string) bool {
	if a.cfg.DisabledTools[n] {
		return false
	}
	t, ok := a.cfg.Tools.Get(n)
	if !ok {
		return false
	}
	if h, ok := t.(tools.ModelHider); ok && h.HiddenFromModel() {
		return false
	}
	return true
}

// loadedDefs returns the definitions of the loaded deferred tools this agent has, in load order, leaving out names
// in have (already offered).
func (a *Agent) loadedDefs(bundle *prompts.Bundle, have map[string]bool) []llm.ToolDef {
	if !a.canSearch() {
		return nil
	}
	var out []llm.ToolDef
	for _, n := range a.cfg.Deferral.Order() {
		if have[n] || !a.offerable(n) {
			continue
		}
		if a.cfg.Deferral.DefersBuiltin(n) {
			if defs := bundle.ToolDefs([]string{n}); len(defs) == 1 {
				out = append(out, defs[0])
				have[n] = true
			}
			continue
		}
		if d, ok := a.cfg.Deferral.Def(n); ok {
			out = append(out, d)
			have[n] = true
		}
	}
	return out
}

// DeferredReminder returns the vars of the deferred_tools reminder for this agent: the deferred tools it has and has
// not loaded, with their summaries. Nil when there are none or the agent does not defer.
func (a *Agent) DeferredReminder() map[string]string {
	if !a.canSearch() {
		return nil
	}
	var lines []string
	for _, d := range a.cfg.Deferral.Pending() {
		if !a.offerable(d.Name) {
			continue
		}
		if s := toolsearch.Summary(d.Description); s != "" {
			lines = append(lines, d.Name+": "+s)
		} else {
			lines = append(lines, d.Name)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return map[string]string{"tools": strings.Join(lines, "\n")}
}

// remindDeferred queues the list of deferred tools (session start, after a compaction).
func (a *Agent) remindDeferred() {
	if vars := a.DeferredReminder(); vars != nil {
		a.Remind(toolsearch.ReminderDeferredTools, vars)
	}
}

// loadOnCall handles a call of a deferred built-in tool the model did not load. The call is not refused: the system
// prompt, skills and reminders name tools the model is expected to call directly, and an error that only says "load
// it first" would cost a round trip each time. The input is checked against the tool's schema first, since a guess
// at the parameters must not run with defaults: valid input loads the tool and the call goes on; invalid input loads
// the tool too and returns its definition with the error, so the next attempt is informed. It returns a result to
// send back instead of running the call, or nil to run it.
func (a *Agent) loadOnCall(u llm.ContentBlock) *tools.Result {
	if !a.canSearch() || !a.cfg.Deferral.DefersBuiltin(u.Name) || a.cfg.Deferral.Loaded(u.Name) {
		return nil
	}
	defs := a.Bundle().ToolDefs([]string{u.Name})
	if len(defs) != 1 {
		return nil
	}
	a.cfg.Deferral.Load(u.Name)
	problem := validateInput(defs[0].InputSchema, u.Input)
	if problem == "" {
		return nil
	}
	r := tools.ErrorResult("InputValidationError: %s\n%s was not loaded, so its parameters were guessed. It is loaded now; its definition:\n%s\nCall it again with input that matches.",
		problem, u.Name, toolsearch.Functions(defs))
	return &r
}

// validateInput checks a tool input against its JSON schema; "" when it is valid (or the schema cannot be used).
func validateInput(schema, input json.RawMessage) string {
	var s jsonschema.Schema
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil {
		return ""
	}
	r, err := s.Resolve(nil)
	if err != nil {
		return ""
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return fmt.Sprintf("input is not valid JSON: %v", err)
	}
	if err := r.Validate(v); err != nil {
		return err.Error()
	}
	return ""
}
