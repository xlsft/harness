package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/xlsft/harness/packages/client/permissions"
)

// Tools for the model to read and change the session and a few user preferences. The model can never raise its own
// permissions: the settings tools reach only the fixed allowlist below and every change goes through a prompt only the
// user can answer; a mode switch to a more permissive mode does too, and bypass is refused outright.

// SessionControl is implemented by an Interactor that owns the session (AgentService): the same code paths as the RPCs
// the frontends call. Changes reach the frontends as SessionUpdated.
type SessionControl interface {
	SessionModel() string
	// SwitchModel applies from the next model request and returns the model's display name.
	SwitchModel(ctx context.Context, model string) (string, error)
	SessionEffort() string
	SwitchEffort(effort string) error
	SessionTitle() string
	// RenameSession returns the title as stored.
	RenameSession(ctx context.Context, title string) (string, error)
	SetKeepAwake(on bool) error
	// Setting returns a value of the allowlist, in its normal form.
	Setting(ctx context.Context, key string) (string, error)
	// ApplySetting changes a value of the allowlist, already normalized.
	ApplySetting(ctx context.Context, key, value string) error
	// NoteChange puts a notice line in the transcript.
	NoteChange(kind, text string)
}

func sessionControl(env *Env) SessionControl {
	h := env.Host()
	if h == nil {
		return nil
	}
	c, _ := h.Interactor().(SessionControl)
	return c
}

// ---- The settings allowlist ----

// Keys of the settings the model may read and, with the user's approval, change.
const (
	SettingTheme               = "theme"
	SettingNotifications       = "notifications"
	SettingOutputStyle         = "outputStyle"
	SettingKeepAwake           = "keepAwake"
	SettingCompactIdleSessions = "compactIdleSessions"
	SettingInterruptReply      = "interruptReply"
)

// SettingDef describes one setting of the allowlist.
type SettingDef struct {
	Key         string
	Description string
	// Scope is where the value lives: "device" stays on this machine, "session" with the session, "user" in the
	// user's synced settings.
	Scope string
	// Kind is "bool", "enum" (Values) or "string" (checked where it is applied).
	Kind   string
	Values []string
}

// SettingDefs is the whole allowlist. Anything else is refused by name in code. It must stay free of what guards the
// user: permission rules and modes, allowed sites, API keys and providers, bypass, computer use, remote control,
// commands to run (an editor setting would be one), and data retention.
var SettingDefs = []SettingDef{
	{Key: SettingTheme, Description: "Color theme of the app on this device", Scope: "device", Kind: "enum", Values: []string{"system", "light", "dark"}},
	{Key: SettingNotifications, Description: "System notifications when a session needs the user, on this device", Scope: "device", Kind: "bool"},
	{Key: SettingOutputStyle, Description: "Output style of this session (a style id or name)", Scope: "session", Kind: "string"},
	{Key: SettingKeepAwake, Description: "Hold off the computer's sleep while this session works", Scope: "session", Kind: "bool"},
	{Key: SettingCompactIdleSessions, Description: "Compact sessions in the background after they were idle for a while", Scope: "user", Kind: "bool"},
	{Key: SettingInterruptReply, Description: "Let the model answer a stopped turn with a short message; sessions opened from now on", Scope: "user", Kind: "bool"},
}

func settingDef(key string) (SettingDef, bool) {
	i := slices.IndexFunc(SettingDefs, func(d SettingDef) bool { return d.Key == key })
	if i < 0 {
		return SettingDef{}, false
	}
	return SettingDefs[i], true
}

func settingKeys() string {
	keys := make([]string, len(SettingDefs))
	for i, d := range SettingDefs {
		keys[i] = d.Key
	}
	return strings.Join(keys, ", ")
}

func unknownSetting(key string) string {
	return fmt.Sprintf("%q is not reachable from here: these tools only cover %s. Security settings (permission rules and modes, allowed sites, API keys, bypass, computer use) can be changed only by the user, in the app.", key, settingKeys())
}

// normalizeSetting checks a value against its definition and returns its normal form.
func normalizeSetting(d SettingDef, raw json.RawMessage) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		return "", fmt.Errorf("InputValidationError: value is required")
	}
	s := ""
	switch x := v.(type) {
	case bool:
		s = strconv.FormatBool(x)
	case string:
		s = strings.TrimSpace(x)
	default:
		return "", fmt.Errorf("InputValidationError: value of %s must be a string or a boolean", d.Key)
	}
	switch d.Kind {
	case "bool":
		if s != "true" && s != "false" {
			return "", fmt.Errorf("InputValidationError: %s is true or false, got %q", d.Key, s)
		}
	case "enum":
		if !slices.Contains(d.Values, s) {
			return "", fmt.Errorf("InputValidationError: %s is one of %s, got %q", d.Key, strings.Join(d.Values, ", "), s)
		}
	default:
		if s == "" {
			return "", fmt.Errorf("InputValidationError: %s must not be empty", d.Key)
		}
	}
	return s, nil
}

// ---- SettingsGet ----

// SettingsGet reads the settings of the allowlist.
type SettingsGet struct{}

func (*SettingsGet) Name() string                  { return "SettingsGet" }
func (*SettingsGet) ReadOnly(json.RawMessage) bool { return true }

func (*SettingsGet) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Key string `json:"key"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	defs := SettingDefs
	if in.Key != "" {
		d, ok := settingDef(in.Key)
		if !ok {
			return ErrorResult("%s", unknownSetting(in.Key))
		}
		defs = []SettingDef{d}
	}
	c := sessionControl(env)
	if c == nil {
		return ErrorResult("Settings are not available in this session")
	}
	type row struct {
		Key         string   `json:"key"`
		Value       string   `json:"value,omitempty"`
		Error       string   `json:"error,omitempty"`
		Scope       string   `json:"scope"`
		Description string   `json:"description"`
		Allowed     []string `json:"allowed,omitempty"`
	}
	rows := make([]row, 0, len(defs))
	for _, d := range defs {
		r := row{Key: d.Key, Scope: d.Scope, Description: d.Description, Allowed: d.Values}
		if d.Kind == "bool" {
			r.Allowed = []string{"true", "false"}
		}
		v, err := c.Setting(ctx, d.Key)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Value = v
		}
		rows = append(rows, r)
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	res := TextResult(string(b), map[string]any{"settings": rows})
	res.Summary = fmt.Sprintf("Read %d settings", len(rows))
	if len(rows) == 1 {
		res.Summary = "Read " + rows[0].Key
	}
	return res
}

// ---- SettingsSet ----

// SettingsSet changes one setting of the allowlist; the user approves each change.
type SettingsSet struct{}

func (*SettingsSet) Name() string                  { return "SettingsSet" }
func (*SettingsSet) ReadOnly(json.RawMessage) bool { return false }

type settingsSetInput struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// PermissionHints implements PermissionHinter: a valid change always asks the user, with the old and the new value,
// whatever the mode (plan mode included: this does not touch the work). What Run refuses anyway is not worth a prompt.
func (*SettingsSet) PermissionHints(ctx context.Context, env *Env, call Call) PermissionHints {
	var in settingsSetInput
	refused := PermissionHints{ReadOnly: true}
	if decode(call.Input, &in) != nil {
		return refused
	}
	d, ok := settingDef(in.Key)
	if !ok {
		return refused
	}
	value, err := normalizeSetting(d, in.Value)
	c := sessionControl(env)
	if err != nil || c == nil {
		return refused
	}
	old, err := c.Setting(ctx, d.Key)
	if err != nil {
		old = "unknown"
	}
	return PermissionHints{ReadOnly: true, Sensitive: fmt.Sprintf("Change %s from %s to %s (%s).", d.Key, strconv.Quote(old), strconv.Quote(value), d.Description)}
}

func (*SettingsSet) Run(ctx context.Context, env *Env, call Call) Result {
	var in settingsSetInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	d, ok := settingDef(in.Key)
	if !ok {
		return ErrorResult("%s", unknownSetting(in.Key))
	}
	value, err := normalizeSetting(d, in.Value)
	if err != nil {
		return ErrorResult("%v", err)
	}
	c := sessionControl(env)
	if c == nil {
		return ErrorResult("Settings are not available in this session")
	}
	old, oldErr := c.Setting(ctx, d.Key)
	if err := c.ApplySetting(ctx, d.Key, value); err != nil {
		return ErrorResult("%v", err)
	}
	if oldErr != nil {
		old = ""
	}
	c.NoteChange("setting_changed", fmt.Sprintf("%s set to %s (changed by the agent, approved by you).", d.Key, value))
	r := TextResult(fmt.Sprintf("%s is now %s.", d.Key, value), map[string]any{"key": d.Key, "oldValue": old, "newValue": value})
	r.Summary = d.Key + " = " + value
	return r
}

// ---- SessionSetModel ----

// SessionSetModel switches the model of the session.
type SessionSetModel struct{}

func (*SessionSetModel) Name() string                  { return "SessionSetModel" }
func (*SessionSetModel) ReadOnly(json.RawMessage) bool { return true }

func (*SessionSetModel) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Model string `json:"model"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.Model = strings.TrimSpace(in.Model); in.Model == "" {
		return ErrorResult("InputValidationError: model is required")
	}
	c := sessionControl(env)
	if c == nil {
		return ErrorResult("The model cannot be changed in this session")
	}
	old := c.SessionModel()
	if old == in.Model {
		return TextResult("The session already uses "+in.Model+".", map[string]any{"model": in.Model})
	}
	display, err := c.SwitchModel(ctx, in.Model)
	if err != nil {
		return ErrorResult("cannot switch to %s: %v", in.Model, err)
	}
	c.NoteChange("session_model_changed", "Model changed to "+display+" by the agent.")
	r := TextResult(fmt.Sprintf("The session now uses %s, from the next model request.", in.Model), map[string]any{"oldModel": old, "model": in.Model})
	r.Summary = "Model: " + display
	return r
}

// ---- SessionSetEffort ----

// SessionSetEffort changes the effort level of the session.
type SessionSetEffort struct{}

func (*SessionSetEffort) Name() string                  { return "SessionSetEffort" }
func (*SessionSetEffort) ReadOnly(json.RawMessage) bool { return true }

func (*SessionSetEffort) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Effort string `json:"effort"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	in.Effort = strings.ToLower(strings.TrimSpace(in.Effort))
	if in.Effort == "" {
		return ErrorResult("InputValidationError: effort is required")
	}
	c := sessionControl(env)
	if c == nil {
		return ErrorResult("The effort cannot be changed in this session")
	}
	old := c.SessionEffort()
	if err := c.SwitchEffort(in.Effort); err != nil {
		return ErrorResult("%v", err)
	}
	if old != in.Effort {
		c.NoteChange("session_effort_changed", "Effort changed to "+in.Effort+" by the agent.")
	}
	r := TextResult(fmt.Sprintf("The effort is now %s, from the next model request.", in.Effort), map[string]any{"oldEffort": old, "effort": in.Effort})
	r.Summary = "Effort: " + in.Effort
	return r
}

// ---- SessionSetTitle ----

// SessionSetTitle renames the session. The title counts as a rename: the automatic title never replaces it.
type SessionSetTitle struct{}

func (*SessionSetTitle) Name() string                  { return "SessionSetTitle" }
func (*SessionSetTitle) ReadOnly(json.RawMessage) bool { return true }

func (*SessionSetTitle) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Title string `json:"title"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Title) == "" {
		return ErrorResult("InputValidationError: title is required")
	}
	c := sessionControl(env)
	if c == nil {
		return ErrorResult("The title cannot be changed in this session")
	}
	old := c.SessionTitle()
	title, err := c.RenameSession(ctx, in.Title)
	if err != nil {
		return ErrorResult("cannot rename the session: %v", err)
	}
	if old != title {
		c.NoteChange("session_title_changed", "Session renamed to "+strconv.Quote(title)+" by the agent.")
	}
	r := TextResult("The session is now titled "+strconv.Quote(title)+".", map[string]any{"oldTitle": old, "title": title})
	r.Summary = "Renamed the session"
	return r
}

// ---- SessionSetMode ----

// SessionSetMode changes the permission mode. It is not read-only, so that each call is checked and run on its own:
// the check compares against the mode as it is right when the call runs, not as it was when a batch started.
type SessionSetMode struct{}

func (*SessionSetMode) Name() string                  { return "SessionSetMode" }
func (*SessionSetMode) ReadOnly(json.RawMessage) bool { return false }

func parseTargetMode(input json.RawMessage) (permissions.Mode, error) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Mode) == "" {
		return "", fmt.Errorf("InputValidationError: mode is required")
	}
	return permissions.ParseMode(in.Mode)
}

// PermissionHints implements PermissionHinter: a move to a more permissive mode only the user may approve, whatever
// the rules and the current mode say; a move to a stricter one, or what Run refuses, passes the check.
func (*SessionSetMode) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	free := PermissionHints{ReadOnly: true}
	to, err := parseTargetMode(call.Input)
	h := env.Host()
	if err != nil || h == nil || to == permissions.ModeBypassPermissions {
		return free
	}
	from := permissions.Mode(h.PermissionMode())
	if !permissions.WidensMode(from, to) {
		return free
	}
	return PermissionHints{ReadOnly: true, Sensitive: fmt.Sprintf("Switch this session from %s mode to %s mode, which needs fewer approvals.", permissions.ModeLabel(from), permissions.ModeLabel(to))}
}

func (*SessionSetMode) Run(ctx context.Context, env *Env, call Call) Result {
	to, err := parseTargetMode(call.Input)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if to == permissions.ModeBypassPermissions {
		return ErrorResult("Bypass mode cannot be entered from a tool: only the user can turn it on, in the app.")
	}
	h := env.Host()
	if h == nil {
		return ErrorResult("The permission mode cannot be changed in this session")
	}
	from := permissions.Mode(h.PermissionMode())
	if from == to {
		return TextResult(permissions.ModeLabel(to)+" mode is already active.", map[string]any{"mode": string(to)})
	}
	if err := h.SetPermissionMode(string(to)); err != nil {
		return ErrorResult("cannot switch to %s mode: %v", permissions.ModeLabel(to), err)
	}
	if c := sessionControl(env); c != nil {
		c.NoteChange("session_mode_changed", fmt.Sprintf("Permission mode changed from %s to %s by the agent.", permissions.ModeLabel(from), permissions.ModeLabel(to)))
	}
	structured := map[string]any{"oldMode": string(from), "mode": string(to)}
	text := fmt.Sprintf("Switched to %s mode, from the next tool call.", permissions.ModeLabel(to))
	if to == permissions.ModePlan {
		// The same state as EnterPlanMode: ExitPlanMode presents the plan and returns to the mode this came from.
		path := NextPlanPath(env)
		h.Remind("plan_mode", map[string]string{"plan_file_path": path})
		structured["planFilePath"] = path
		text = "Entered plan mode. Explore with read-only tools, then present the plan with ExitPlanMode."
	}
	r := TextResult(text, structured)
	r.Summary = "Mode: " + permissions.ModeLabel(to)
	return r
}
