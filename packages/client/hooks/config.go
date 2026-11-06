// Package hooks runs the hooks of settings files: programs or web endpoints the user or an administrator configured, which
// the client starts at points of a session. They see those points as JSON and can answer: refuse a tool call, rewrite its
// input, add context for the model, keep the agent going. See docs/design/hooks.md for the contract.
//
// The package knows the settings format and how to run a handler. Where each event fires is the agent loop's and the
// session's business: they call Engine.Dispatch and act on the Outcome.
package hooks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/xlsft/harness/packages/client/settings"
)

// Event is a hook event, named as in the settings files.
type Event string

// The events that fire. Each is documented where it fires; docs/design/hooks.md lists what each can do.
const (
	SessionStart       Event = "SessionStart"
	SessionEnd         Event = "SessionEnd"
	UserPromptSubmit   Event = "UserPromptSubmit"
	PreToolUse         Event = "PreToolUse"
	PostToolUse        Event = "PostToolUse"
	PostToolUseFailure Event = "PostToolUseFailure"
	PostToolBatch      Event = "PostToolBatch"
	PermissionRequest  Event = "PermissionRequest"
	PermissionDenied   Event = "PermissionDenied"
	Notification       Event = "Notification"
	SubagentStart      Event = "SubagentStart"
	SubagentStop       Event = "SubagentStop"
	Stop               Event = "Stop"
	StopFailure        Event = "StopFailure"
	PreCompact         Event = "PreCompact"
	PostCompact        Event = "PostCompact"
	CwdChanged         Event = "CwdChanged"
)

// matcherOn says what an event's matcher is compared with; events without an entry ignore their matcher.
var matcherOn = map[Event]string{
	PreToolUse: "tool name", PostToolUse: "tool name", PostToolUseFailure: "tool name", PermissionRequest: "tool name", PermissionDenied: "tool name",
	SessionStart: "source", SessionEnd: "reason", PreCompact: "trigger", PostCompact: "trigger", Notification: "notification type",
	SubagentStart: "agent type", SubagentStop: "agent type", StopFailure: "error type",
}

// noMatcher lists the events that fire and take no matcher.
var noMatcher = map[Event]bool{UserPromptSubmit: true, Stop: true, PostToolBatch: true, CwdChanged: true}

// Supported reports whether this client fires ev.
func Supported(ev Event) bool {
	_, ok := matcherOn[ev]
	return ok || noMatcher[ev]
}

// Handler types.
const (
	TypeCommand = "command"
	TypeHTTP    = "http"
)

// Status says whether a configured hook runs.
type Status int

const (
	StatusActive Status = iota
	StatusDisabled
	StatusNeedsWorkspaceTrust
	StatusNeedsDeviceTrust
	StatusBlockedByPolicy
	StatusUnsupported
)

// Hook is one configured handler.
type Hook struct {
	Event   Event
	Matcher string
	Type    string
	// Command is the command line (command) or the URL (http).
	Command string
	Headers map[string]string
	// AllowedEnvVars are the variables Headers may name.
	AllowedEnvVars []string
	// Timeout is the configured limit (0: the default of its kind).
	Timeout time.Duration
	Async   bool

	Source settings.Source
	// TrustHash is set for what the server delivered: the value device trust is keyed on.
	TrustHash string
	Status    Status
	// Reason says why it does not run.
	Reason string

	match func(string) bool
}

// Set is the hooks a session runs: what its settings said when it opened or moved. A later change of the files applies to
// the next session; the model cannot give itself a hook in a session it is running.
type Set struct {
	// All is every configured hook, lowest precedence first, including those that do not run.
	All []*Hook
	// Off and PolicyOff are the switches when the set was built; Engine reads them again for every event.
	Off, PolicyOff bool
}

// Build reads the hooks of merged settings. trusted says whether content the server delivered is trusted on this machine
// (nil: nothing is).
func Build(st *settings.Settings, trusted func(hash string) bool) *Set {
	set := &Set{}
	if st == nil {
		return set
	}
	set.Off, set.PolicyOff = st.HooksOff, st.HooksPolicyOff
	for _, l := range st.Hooks {
		set.All = append(set.All, parseLayer(l, trusted)...)
	}
	return set
}

// parseLayer turns a block into hooks. Nothing here is fatal: what cannot run stays in the list with the reason.
func parseLayer(l settings.HookLayer, trusted func(string) bool) []*Hook {
	gated := func(h *Hook) *Hook {
		h.Source = l.Source
		if l.Gate == settings.HookGateDevice {
			h.TrustHash = l.Hash
		}
		if h.Status != StatusActive {
			return h
		}
		switch l.Gate {
		case settings.HookGateWorkspace:
			h.Status, h.Reason = StatusNeedsWorkspaceTrust, "This folder is not trusted on this machine."
		case settings.HookGateManaged:
			h.Status, h.Reason = StatusBlockedByPolicy, "A policy allows only its own hooks."
		case settings.HookGateDevice:
			if trusted == nil || !trusted(l.Hash) {
				h.Status, h.Reason = StatusNeedsDeviceTrust, "It came from the server and is not trusted on this machine yet."
			}
		}
		return h
	}
	bad := func(ev Event, format string, args ...any) *Hook {
		return gated(&Hook{Event: ev, Status: StatusUnsupported, Reason: fmt.Sprintf(format, args...)})
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(l.Raw, &events); err != nil {
		return []*Hook{bad("", "The hooks setting must be an object of events: %v", err)}
	}
	var out []*Hook
	for _, name := range sortedKeys(events) {
		ev := Event(name)
		var groups []struct {
			Matcher json.RawMessage   `json:"matcher"`
			Hooks   []json.RawMessage `json:"hooks"`
		}
		if err := json.Unmarshal(events[name], &groups); err != nil {
			out = append(out, bad(ev, "An event must hold a list of matcher groups: %v", err))
			continue
		}
		for _, g := range groups {
			var matcher string
			if len(g.Matcher) > 0 && json.Unmarshal(g.Matcher, &matcher) != nil {
				out = append(out, bad(ev, "The matcher must be a string."))
				continue
			}
			match, err := compileMatcher(matcher)
			for _, raw := range g.Hooks {
				h := parseHandler(ev, raw)
				h.Matcher, h.match = matcher, match
				switch {
				case h.Status != StatusActive:
				case !Supported(ev):
					h.Status, h.Reason = StatusUnsupported, "This client does not fire the event "+name+"."
				case err != nil:
					h.Status, h.Reason = StatusUnsupported, fmt.Sprintf("The matcher cannot be read: %v", err)
				}
				out = append(out, gated(h))
			}
		}
	}
	return out
}

func parseHandler(ev Event, raw json.RawMessage) *Hook {
	h := &Hook{Event: ev}
	var d struct {
		Type           string            `json:"type"`
		Command        string            `json:"command"`
		URL            string            `json:"url"`
		Headers        map[string]string `json:"headers"`
		AllowedEnvVars []string          `json:"allowedEnvVars"`
		Timeout        float64           `json:"timeout"`
		Async          bool              `json:"async"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		h.Status, h.Reason = StatusUnsupported, fmt.Sprintf("The handler cannot be read: %v", err)
		return h
	}
	h.Type, h.Headers, h.AllowedEnvVars, h.Async = d.Type, d.Headers, d.AllowedEnvVars, d.Async
	h.Timeout = time.Duration(d.Timeout * float64(time.Second))
	switch d.Type {
	case TypeCommand:
		h.Command = d.Command
		if strings.TrimSpace(d.Command) == "" {
			h.Status, h.Reason = StatusUnsupported, "A command handler needs a command."
		}
	case TypeHTTP:
		h.Command = d.URL
		if !strings.HasPrefix(d.URL, "http://") && !strings.HasPrefix(d.URL, "https://") {
			h.Status, h.Reason = StatusUnsupported, "An http handler needs an http or https URL."
		}
	case "":
		h.Status, h.Reason = StatusUnsupported, "The handler has no type."
	default:
		h.Status, h.Reason = StatusUnsupported, "This client does not run handlers of type "+d.Type+"."
	}
	if d.Timeout < 0 {
		h.Status, h.Reason = StatusUnsupported, "The timeout must not be negative."
	}
	return h
}

// simpleMatcher is a matcher of plain names: compared whole, not searched for.
var simpleMatcher = regexp.MustCompile(`^[A-Za-z0-9_|]+$`)

// compileMatcher understands the matcher forms of the settings format: empty or "*" for everything, names separated by "|"
// compared whole, and otherwise a regular expression searched for in the value. Expressions RE2 cannot read (lookaround)
// are reported; such a hook does not run.
func compileMatcher(m string) (func(string) bool, error) {
	if m == "" || m == "*" {
		return func(string) bool { return true }, nil
	}
	if simpleMatcher.MatchString(m) {
		names := strings.Split(m, "|")
		return func(s string) bool {
			for _, n := range names {
				if n == s {
					return true
				}
			}
			return false
		}, nil
	}
	re, err := regexp.Compile(m)
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

// Matching returns the hooks that run for an event and the value its matcher looks at, one per distinct handler: hooks
// with the same type and command run once, whichever levels name them, under the highest of those levels.
func (s *Set) Matching(ev Event, value string) []*Hook {
	if s == nil {
		return nil
	}
	var out []*Hook
	seen := map[string]int{}
	for _, h := range s.All {
		if h.Event != ev || h.Status != StatusActive || (!noMatcher[ev] && !h.match(value)) {
			continue
		}
		key := h.Type + "\x00" + h.Command
		if i, ok := seen[key]; ok {
			out[i] = h
			continue
		}
		seen[key] = len(out)
		out = append(out, h)
	}
	return out
}

// Has reports whether anything runs for an event, whatever its matcher looks at.
func (s *Set) Has(ev Event) bool {
	if s == nil {
		return false
	}
	for _, h := range s.All {
		if h.Event == ev && h.Status == StatusActive {
			return true
		}
	}
	return false
}
