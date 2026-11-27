package simulator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xlsft/harness/packages/client/computeruse"
)

// Input sends touches, keys and buttons to a booted simulator. Points are fractions of the screen, 0 to 1.
type Input interface {
	Touch(ctx context.Context, udid string, points []Point, holdMS, stepMS int) error
	// Keys presses HID keyboard usages; each press is a usage and the modifier usages held around it.
	Keys(ctx context.Context, udid string, presses []Press) error
	Button(ctx context.Context, udid, button string, holdMS int) error
}

// Point is a position as a fraction of the device's screen.
type Point struct{ X, Y float64 }

// Press is one key press: Usage of the HID keyboard page, with Mods held down around it.
type Press struct {
	Usage int
	Mods  []int
}

// ErrNoInput: this machine cannot send input to a simulator.
var ErrNoInput = errors.New("input to the iOS Simulator needs the computer use helper, which ships with the macOS app (or build it with packages/client/computeruse/helper/build.sh)")

// helperInput is Input over the computer use helper (helper/main.swift, the sim_* operations).
type helperInput struct {
	h   computeruse.Helper
	dir func(context.Context) string
}

func (in *helperInput) params(ctx context.Context, udid string, extra map[string]any) map[string]any {
	p := map[string]any{"udid": udid, "developer_dir": in.dir(ctx)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func (in *helperInput) Touch(ctx context.Context, udid string, points []Point, holdMS, stepMS int) error {
	if in.h == nil {
		return ErrNoInput
	}
	raw := make([][]float64, len(points))
	for i, p := range points {
		raw[i] = []float64{p.X, p.Y}
	}
	return in.h.Call(ctx, "sim_touch", in.params(ctx, udid, map[string]any{"points": raw, "hold_ms": holdMS, "step_ms": stepMS}), nil)
}

func (in *helperInput) Keys(ctx context.Context, udid string, presses []Press) error {
	if in.h == nil {
		return ErrNoInput
	}
	raw := make([][]int, len(presses))
	for i, p := range presses {
		raw[i] = append([]int{p.Usage}, p.Mods...)
	}
	return in.h.Call(ctx, "sim_keys", in.params(ctx, udid, map[string]any{"presses": raw}), nil)
}

func (in *helperInput) Button(ctx context.Context, udid, button string, holdMS int) error {
	if in.h == nil {
		return ErrNoInput
	}
	return in.h.Call(ctx, "sim_button", in.params(ctx, udid, map[string]any{"button": button, "hold_ms": holdMS}), nil)
}

// HID usages (keyboard page 0x07) of a US layout.
const (
	usageCtrl  = 224
	usageShift = 225
	usageAlt   = 226
	usageCmd   = 227
	usageV     = 25
)

var modUsages = map[string]int{"ctrl": usageCtrl, "control": usageCtrl, "shift": usageShift, "alt": usageAlt, "option": usageAlt, "opt": usageAlt, "cmd": usageCmd, "command": usageCmd, "meta": usageCmd}

var namedKeys = map[string]int{
	"return": 40, "enter": 40, "escape": 41, "esc": 41, "delete": 42, "backspace": 42, "tab": 43, "space": 44,
	"forward_delete": 76, "home": 74, "page_up": 75, "end": 77, "page_down": 78,
	"right": 79, "left": 80, "down": 81, "up": 82,
}

// The plain and shifted characters of the keys after the letters and digits.
var symbolKeys = map[rune]Press{
	' ': {Usage: 44}, '\n': {Usage: 40}, '\t': {Usage: 43},
	'-': {Usage: 45}, '_': {Usage: 45, Mods: []int{usageShift}}, '=': {Usage: 46}, '+': {Usage: 46, Mods: []int{usageShift}},
	'[': {Usage: 47}, '{': {Usage: 47, Mods: []int{usageShift}}, ']': {Usage: 48}, '}': {Usage: 48, Mods: []int{usageShift}},
	'\\': {Usage: 49}, '|': {Usage: 49, Mods: []int{usageShift}}, ';': {Usage: 51}, ':': {Usage: 51, Mods: []int{usageShift}},
	'\'': {Usage: 52}, '"': {Usage: 52, Mods: []int{usageShift}}, '`': {Usage: 53}, '~': {Usage: 53, Mods: []int{usageShift}},
	',': {Usage: 54}, '<': {Usage: 54, Mods: []int{usageShift}}, '.': {Usage: 55}, '>': {Usage: 55, Mods: []int{usageShift}},
	'/': {Usage: 56}, '?': {Usage: 56, Mods: []int{usageShift}},
}

const shiftedDigits = ")!@#$%^&*("

// keyFor returns the press that types r on a US layout, or false for a character no key types.
func keyFor(r rune) (Press, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return Press{Usage: 4 + int(r-'a')}, true
	case r >= 'A' && r <= 'Z':
		return Press{Usage: 4 + int(r-'A'), Mods: []int{usageShift}}, true
	case r >= '1' && r <= '9':
		return Press{Usage: 30 + int(r-'1')}, true
	case r == '0':
		return Press{Usage: 39}, true
	}
	if i := strings.IndexRune(shiftedDigits, r); i >= 0 {
		if i == 0 {
			return Press{Usage: 39, Mods: []int{usageShift}}, true
		}
		return Press{Usage: 29 + i, Mods: []int{usageShift}}, true
	}
	if p, ok := symbolKeys[r]; ok {
		return p, true
	}
	return Press{}, false
}

// segment is a run of text: keys to press, or text to paste (what no key of a US layout types).
type segment struct {
	presses []Press
	paste   string
}

// segments splits text into key presses and pasted runs, in order.
func segments(text string) []segment {
	var out []segment
	var keys []Press
	var paste strings.Builder
	flushKeys := func() {
		if len(keys) > 0 {
			out = append(out, segment{presses: keys})
			keys = nil
		}
	}
	flushPaste := func() {
		if paste.Len() > 0 {
			out = append(out, segment{paste: paste.String()})
			paste.Reset()
		}
	}
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		if p, ok := keyFor(r); ok {
			flushPaste()
			keys = append(keys, p)
			continue
		}
		flushKeys()
		paste.WriteRune(r)
	}
	flushKeys()
	flushPaste()
	return out
}

// parseKeys reads space-separated combinations ("cmd+a delete return") into presses.
func parseKeys(s string) ([]Press, error) {
	var out []Press
	for _, combo := range strings.Fields(s) {
		parts := strings.Split(strings.ToLower(combo), "+")
		key := parts[len(parts)-1]
		var p Press
		if u, ok := namedKeys[key]; ok {
			p.Usage = u
		} else if r := []rune(key); len(r) == 1 {
			kp, ok := keyFor(r[0])
			if !ok {
				return nil, fmt.Errorf("unknown key %q", combo)
			}
			p = kp
		} else {
			return nil, fmt.Errorf("unknown key %q", combo)
		}
		for _, m := range parts[:len(parts)-1] {
			u, ok := modUsages[m]
			if !ok {
				return nil, fmt.Errorf("unknown modifier %q in %q", m, combo)
			}
			p.Mods = append(p.Mods, u)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("no keys")
	}
	return out, nil
}
