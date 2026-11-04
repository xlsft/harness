package computeruse

import (
	"fmt"
	"slices"
	"strings"
)

// Key combinations: "cmd+shift+s", several separated by spaces. Modifiers are normalized to cmd, ctrl, alt, shift, and
// sorted, so each combination has one spelling for the checks below and for the driver.

var modifierNames = map[string]string{
	"cmd": "cmd", "command": "cmd", "meta": "cmd", "super": "cmd", "win": "cmd",
	"ctrl": "ctrl", "control": "ctrl",
	"alt": "alt", "option": "alt", "opt": "alt",
	"shift": "shift",
}

var keyAliases = map[string]string{
	"return": "enter", "esc": "escape", "del": "delete", "backspace": "delete", "forwarddelete": "forward_delete",
	"arrowup": "up", "arrowdown": "down", "arrowleft": "left", "arrowright": "right", "spacebar": "space", "pgup": "page_up",
	"pgdn": "page_down", "pageup": "page_up", "pagedown": "page_down",
}

// ParseCombo returns the normalized spelling of one combination.
func ParseCombo(s string) (string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	var mods []string
	key := ""
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if m, ok := modifierNames[p]; ok && i < len(parts)-1 {
			if !slices.Contains(mods, m) {
				mods = append(mods, m)
			}
			continue
		}
		if key != "" || p == "" {
			return "", fmt.Errorf("invalid key combination %q", s)
		}
		if a, ok := keyAliases[p]; ok {
			p = a
		}
		key = p
	}
	if key == "" {
		return "", fmt.Errorf("invalid key combination %q", s)
	}
	slices.Sort(mods)
	return strings.Join(append(mods, key), "+"), nil
}

// blockedCombos reach past the app in front: they switch apps, open a system-wide search that takes the following
// typing, or end the user's work. Refused on every tier. To change app the model uses ComputerActivate, which is checked.
var blockedCombos = map[string]string{
	"cmd+space":            "opens system search",
	"alt+cmd+space":        "opens a system search window",
	"ctrl+space":           "switches the input source",
	"cmd+tab":              "switches apps",
	"cmd+shift+tab":        "switches apps",
	"cmd+`":                "switches windows outside the checked path",
	"alt+cmd+escape":       "opens Force Quit",
	"alt+cmd+shift+escape": "force quits the front app",
	"cmd+ctrl+q":           "locks the screen",
	"cmd+shift+q":          "logs out",
	"alt+cmd+shift+q":      "logs out",
	"cmd+ctrl+power":       "restarts the machine",
	"ctrl+up":              "opens Mission Control",
	"ctrl+down":            "opens App Expose",
	"ctrl+left":            "switches Spaces",
	"ctrl+right":           "switches Spaces",
	"cmd+shift+3":          "takes a full screen capture",
	"cmd+shift+4":          "takes a screen capture",
	"cmd+shift+5":          "takes a screen capture",
	"cmd+ctrl+shift+3":     "copies a full screen capture",
	"cmd+ctrl+shift+4":     "copies a screen capture",
	"fn+f":                 "changes full screen",
}

// BlockedCombo returns why a normalized combination is refused ("" if it is not).
func BlockedCombo(combo string) string { return blockedCombos[combo] }

// harmlessInSecureField can be pressed while a secure field has the focus: they move around or leave it and put no
// text in it.
var harmlessInSecureField = map[string]bool{
	"tab": true, "shift+tab": true, "escape": true, "up": true, "down": true, "left": true, "right": true, "enter": true,
}
