package browser

import (
	"context"
	"fmt"
	"strings"
)

// Modifier bits of CDP Input.dispatch*.
const (
	modAlt   = 1
	modCtrl  = 2
	modMeta  = 4
	modShift = 8
)

// keyDef describes a key for Input.dispatchKeyEvent.
type keyDef struct {
	key, code string
	keyCode   int
	text      string
	shifted   string // key and text with Shift, for printable keys
}

var keyTable = buildKeyTable()

func buildKeyTable() map[string]keyDef {
	t := map[string]keyDef{}
	for c := 'a'; c <= 'z'; c++ {
		s := string(c)
		t[s] = keyDef{key: s, code: "Key" + strings.ToUpper(s), keyCode: int(c - 'a' + 65), text: s, shifted: strings.ToUpper(s)}
	}
	shiftedDigits := ")!@#$%^&*("
	for d := '0'; d <= '9'; d++ {
		s := string(d)
		t[s] = keyDef{key: s, code: "Digit" + s, keyCode: int(d), text: s, shifted: string(shiftedDigits[d-'0'])}
	}
	punct := []keyDef{
		{"-", "Minus", 189, "-", "_"}, {"=", "Equal", 187, "=", "+"}, {",", "Comma", 188, ",", "<"},
		{".", "Period", 190, ".", ">"}, {"/", "Slash", 191, "/", "?"}, {";", "Semicolon", 186, ";", ":"},
		{"'", "Quote", 222, "'", "\""}, {"[", "BracketLeft", 219, "[", "{"}, {"]", "BracketRight", 221, "]", "}"},
		{"\\", "Backslash", 220, "\\", "|"}, {"`", "Backquote", 192, "`", "~"},
	}
	for _, k := range punct {
		t[k.key] = k
	}
	named := []keyDef{
		{key: "Enter", code: "Enter", keyCode: 13, text: "\r"},
		{key: "Tab", code: "Tab", keyCode: 9},
		{key: "Backspace", code: "Backspace", keyCode: 8},
		{key: "Delete", code: "Delete", keyCode: 46},
		{key: "Escape", code: "Escape", keyCode: 27},
		{key: " ", code: "Space", keyCode: 32, text: " "},
		{key: "ArrowLeft", code: "ArrowLeft", keyCode: 37},
		{key: "ArrowUp", code: "ArrowUp", keyCode: 38},
		{key: "ArrowRight", code: "ArrowRight", keyCode: 39},
		{key: "ArrowDown", code: "ArrowDown", keyCode: 40},
		{key: "Home", code: "Home", keyCode: 36},
		{key: "End", code: "End", keyCode: 35},
		{key: "PageUp", code: "PageUp", keyCode: 33},
		{key: "PageDown", code: "PageDown", keyCode: 34},
		{key: "Insert", code: "Insert", keyCode: 45},
		{key: "Control", code: "ControlLeft", keyCode: 17},
		{key: "Shift", code: "ShiftLeft", keyCode: 16},
		{key: "Alt", code: "AltLeft", keyCode: 18},
		{key: "Meta", code: "MetaLeft", keyCode: 91},
	}
	for i := 1; i <= 12; i++ {
		n := fmt.Sprintf("F%d", i)
		named = append(named, keyDef{key: n, code: n, keyCode: 111 + i})
	}
	for _, k := range named {
		t[strings.ToLower(k.key)] = k
	}
	aliases := map[string]string{
		"return": "enter", "esc": "escape", "del": "delete", "space": " ", "spacebar": " ",
		"up": "arrowup", "down": "arrowdown", "left": "arrowleft", "right": "arrowright",
		"pgup": "pageup", "pgdn": "pagedown", "pagedn": "pagedown", "ins": "insert", "plus": "=",
	}
	for a, k := range aliases {
		t[a] = t[k]
	}
	return t
}

var modifierNames = map[string]int{
	"ctrl": modCtrl, "control": modCtrl, "shift": modShift, "alt": modAlt, "option": modAlt, "opt": modAlt,
	"meta": modMeta, "cmd": modMeta, "command": modMeta, "super": modMeta, "win": modMeta,
}

// keyPress is one parsed key combination.
type keyPress struct {
	def  keyDef
	mods int
	// name is the combination as written, for labels.
	name string
}

// parseKeys parses a space-separated sequence of combinations such as "ctrl+a Backspace Shift+Tab". A
// "+" key is written "plus" (with Shift for "+").
func parseKeys(s string) ([]keyPress, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("keys is empty")
	}
	var out []keyPress
	for _, f := range fields {
		parts := strings.Split(f, "+")
		if strings.HasSuffix(f, "++") { // "ctrl++"
			parts = append(strings.Split(strings.TrimSuffix(f, "++"), "+"), "plus")
		}
		p := keyPress{name: f}
		for i, part := range parts {
			low := strings.ToLower(part)
			if i < len(parts)-1 {
				m, ok := modifierNames[low]
				if !ok {
					return nil, fmt.Errorf("unknown modifier %q in %q (use ctrl, shift, alt or meta)", part, f)
				}
				p.mods |= m
				continue
			}
			if m, ok := modifierNames[low]; ok && len(parts) == 1 {
				// A modifier on its own.
				name := map[int]string{modCtrl: "control", modShift: "shift", modAlt: "alt", modMeta: "meta"}[m]
				p.def = keyTable[name]
				continue
			}
			def, ok := keyTable[low]
			if !ok && len(part) == 1 {
				def, ok = keyTable[part]
			}
			if !ok {
				return nil, fmt.Errorf("unknown key %q in %q", part, f)
			}
			// "A" alone means Shift+a.
			if len(part) == 1 && part != low {
				p.mods |= modShift
			}
			p.def = def
		}
		out = append(out, p)
	}
	return out, nil
}

// editingCommands are the editor commands a shortcut runs (headless Chrome does not map them from key events,
// notably on macOS).
func editingCommands(key string, mods int) []string {
	if mods&(modCtrl|modMeta) == 0 || mods&modAlt != 0 {
		return nil
	}
	switch strings.ToLower(key) {
	case "a":
		return []string{"selectAll"}
	case "c":
		return []string{"copy"}
	case "x":
		return []string{"cut"}
	case "v":
		return []string{"paste"}
	case "z":
		if mods&modShift != 0 {
			return []string{"redo"}
		}
		return []string{"undo"}
	case "y":
		return []string{"redo"}
	}
	return nil
}

var modifierKeys = []struct {
	bit  int
	name string
}{{modCtrl, "control"}, {modAlt, "alt"}, {modMeta, "meta"}, {modShift, "shift"}}

// press dispatches one key combination: modifiers down, the key down and up, modifiers up.
func press(ctx context.Context, c *conn, session string, p keyPress) error {
	held := 0
	for _, m := range modifierKeys {
		if p.mods&m.bit == 0 || p.def.key == keyTable[m.name].key {
			continue
		}
		held |= m.bit
		md := keyTable[m.name]
		if err := c.Call(ctx, session, "Input.dispatchKeyEvent", map[string]any{"type": "rawKeyDown", "key": md.key, "code": md.code,
			"windowsVirtualKeyCode": md.keyCode, "modifiers": held}, nil); err != nil {
			return err
		}
	}
	key, text := p.def.key, p.def.text
	if p.mods&modShift != 0 && p.def.shifted != "" {
		key, text = p.def.shifted, p.def.shifted
	}
	down := map[string]any{"type": "rawKeyDown", "key": key, "code": p.def.code, "windowsVirtualKeyCode": p.def.keyCode, "modifiers": p.mods}
	if text != "" && p.mods&(modCtrl|modMeta|modAlt) == 0 {
		down["type"], down["text"], down["unmodifiedText"] = "keyDown", text, text
	}
	if cmds := editingCommands(p.def.key, p.mods); len(cmds) > 0 {
		down["commands"] = cmds
	}
	if err := c.Call(ctx, session, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	if err := c.Call(ctx, session, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "code": p.def.code,
		"windowsVirtualKeyCode": p.def.keyCode, "modifiers": p.mods}, nil); err != nil {
		return err
	}
	for i := len(modifierKeys) - 1; i >= 0; i-- {
		m := modifierKeys[i]
		if held&m.bit == 0 {
			continue
		}
		held &^= m.bit
		md := keyTable[m.name]
		if err := c.Call(ctx, session, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": md.key, "code": md.code,
			"windowsVirtualKeyCode": md.keyCode, "modifiers": held}, nil); err != nil {
			return err
		}
	}
	return nil
}
