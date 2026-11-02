package agent

import (
	"strings"

	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/prompts"
	"github.com/xlsft/harness/packages/client/session"
)

// ReminderOutputStyle is the bundle reminder that carries the active output
// style ({{name}}, {{content}}).
const ReminderOutputStyle = "output_style"

// OutputStyle is the active output style as the agent applies it. A nil
// style (or id "default") is the default style, which adds nothing.
//
// Delivery keeps the prompt cache: the style text goes to the model through
// the output_style reminder, injected into the next user message at session
// start and whenever the style changes, so the system prompt and the tool
// list never change on a switch. The one exception is a style with
// KeepCodingInstructions false: the system sections prompts.CodingSections
// are then omitted from the system prompt. That changes the cache prefix, so
// it is applied only while the conversation is empty (session start, resume
// of an empty session) and after a compaction; a mid-session switch to such
// a style delivers its text right away and drops the sections at the next
// compaction (and a switch away from it restores them at the next
// compaction).
type OutputStyle struct {
	ID                     string
	Name                   string
	Text                   string
	KeepCodingInstructions bool
}

func (s *OutputStyle) isDefault() bool {
	return s == nil || s.ID == "" || strings.EqualFold(s.ID, "default")
}

func (s *OutputStyle) omitsCoding() bool { return !s.isDefault() && !s.KeepCodingInstructions }

// styleID is the id recorded in the transcript ("default" for none).
func styleID(s *OutputStyle) string {
	if s.isDefault() {
		return "default"
	}
	return s.ID
}

// initOutputStyle sets the initial style (New). The reminder is queued
// unless the restored conversation already carries this style's text.
func (a *Agent) initOutputStyle(st *OutputStyle, recorded string) {
	if st.isDefault() {
		st = nil
	}
	a.style = st
	a.omitCoding = st.omitsCoding()
	switch {
	case st != nil:
		if a.hasStyleText(st) {
			a.styleDelivered = true
			return
		}
		a.reminders = append(a.reminders, a.styleReminder(st))
	case recorded != "" && !strings.EqualFold(recorded, "default") && len(a.messages) > 0:
		// The resumed conversation ran with another style: tell the model it ended.
		a.styleDelivered = true
		a.reminders = append(a.reminders, a.styleReminder(nil))
	}
}

// hasStyleText reports whether a user message already contains the rendered
// output_style reminder of st. Callers hold a.mu.
func (a *Agent) hasStyleText(st *OutputStyle) bool {
	text := a.renderStyle(st)
	if text == "" {
		return false
	}
	for _, m := range a.messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if b.Type == llm.BlockText && b.Text == text {
				return true
			}
		}
	}
	return false
}

// styleVars are the output_style reminder placeholders for st (nil: the
// bundle's default style). Callers hold a.mu.
func (a *Agent) styleVars(st *OutputStyle) map[string]string {
	if st.isDefault() {
		name, text := "Default", ""
		if d, ok := a.bundle.OutputStyles["default"]; ok {
			if d.Name != "" {
				name = d.Name
			}
			text = d.Text
		}
		return map[string]string{"name": name, "content": text, "id": "default"}
	}
	name := st.Name
	if name == "" {
		name = st.ID
	}
	return map[string]string{"name": name, "content": st.Text, "id": st.ID}
}

func (a *Agent) styleReminder(st *OutputStyle) pendingReminder {
	return pendingReminder{id: ReminderOutputStyle, vars: a.styleVars(st)}
}

// styleTemplate returns the output_style reminder template; without one in
// the bundle the style text is sent as is.
func styleTemplate(b *prompts.Bundle) string {
	if b != nil {
		if t := b.Reminders[ReminderOutputStyle]; t != "" {
			return t
		}
	}
	return "{{content}}"
}

// renderStyle renders the reminder text for st. Callers hold a.mu.
func (a *Agent) renderStyle(st *OutputStyle) string {
	return prompts.Render(styleTemplate(a.bundle), a.styleVars(st), nil)
}

// OutputStyle returns the active output style (nil for the default).
func (a *Agent) OutputStyle() *OutputStyle {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.style == nil {
		return nil
	}
	c := *a.style
	return &c
}

// CodingInstructionsOmitted reports whether the system prompt currently
// leaves out the coding sections (prompts.CodingSections).
func (a *Agent) CodingInstructionsOmitted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.omitCoding
}

// SetOutputStyle switches the output style (nil or id "default" clears it).
// The style text is delivered with the next model request; the system
// prompt changes only when the conversation is still empty. deferred reports
// that the coding sections will be dropped or restored at the next
// compaction instead. The change is recorded in the transcript.
func (a *Agent) SetOutputStyle(st *OutputStyle) (deferred bool) {
	if st.isDefault() {
		st = nil
	} else {
		c := *st
		st = &c
	}
	a.mu.Lock()
	if sameStyle(a.style, st) {
		a.mu.Unlock()
		return false
	}
	a.style = st
	if len(a.messages) == 0 {
		a.omitCoding = st.omitsCoding()
	} else {
		deferred = a.omitCoding != st.omitsCoding()
	}
	kept := a.reminders[:0]
	for _, r := range a.reminders {
		if r.id != ReminderOutputStyle {
			kept = append(kept, r)
		}
	}
	a.reminders = kept
	if st != nil || a.styleDelivered {
		a.reminders = append(a.reminders, a.styleReminder(st))
	}
	a.mu.Unlock()
	a.record(session.Entry{Type: session.TypeMeta, OutputStyle: styleID(st)})
	return deferred
}

func sameStyle(a, b *OutputStyle) bool {
	if a.isDefault() || b.isDefault() {
		return a.isDefault() == b.isDefault()
	}
	return *a == *b
}

// noteStyleInjected records that an output_style reminder reached the
// conversation. Callers hold a.mu.
func (a *Agent) noteStyleInjected(vars map[string]string) {
	a.styleDelivered = vars["id"] != "default"
}

// afterCompactStyle applies the style to the new conversation: the coding
// sections follow the active style again and its text is re-sent (the
// summary replaced the reminder). Callers hold a.mu.
func (a *Agent) afterCompactStyle() {
	a.omitCoding = a.style.omitsCoding()
	a.styleDelivered = false
	kept := a.reminders[:0]
	for _, r := range a.reminders {
		if r.id != ReminderOutputStyle {
			kept = append(kept, r)
		}
	}
	a.reminders = kept
	if a.style != nil {
		a.reminders = append(a.reminders, a.styleReminder(a.style))
	}
}

// afterReloadStyle re-sends the style when a rewind removed its reminder.
// Callers hold a.mu.
func (a *Agent) afterReloadStyle() {
	if a.style == nil {
		a.styleDelivered = false
		return
	}
	if a.hasStyleText(a.style) {
		a.styleDelivered = true
		return
	}
	a.styleDelivered = false
	for _, r := range a.reminders {
		if r.id == ReminderOutputStyle {
			return
		}
	}
	a.reminders = append(a.reminders, a.styleReminder(a.style))
}
