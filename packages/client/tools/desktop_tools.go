package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// OpenInEditor and RevealPath act on the machine that runs the session: its editor, its file manager. Programs start
// from argv, never through a shell, and are not waited for: they outlive the tool call. A path outside the session's
// scope needs the user's approval (PermissionHints); a session the user drives from another machine refuses, since
// what opens would open where they are not (DeviceOwner).

// DeviceOwner is implemented by an Interactor that knows where the user is.
type DeviceOwner interface {
	// ElsewhereReason says why something opened on this machine would not reach the user, "" when it would.
	ElsewhereReason() string
}

// Desktop is how the tools reach the operating system; tests replace it.
type Desktop struct {
	GOOS     string
	Getenv   func(string) string
	LookPath func(string) (string, error)
	// Start starts a program and does not wait for it.
	Start func(argv []string) error
}

func systemDesktop() *Desktop {
	return &Desktop{GOOS: runtime.GOOS, Getenv: os.Getenv, LookPath: exec.LookPath, Start: startDetached}
}

func startDetached(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	setDesktopCommandLine(cmd, argv)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// terminalEditors cannot open from here: the client has no terminal to run them in.
var terminalEditors = map[string]bool{
	"vi": true, "vim": true, "nvim": true, "nano": true, "pico": true, "emacs": true, "ed": true, "ex": true,
	"micro": true, "hx": true, "helix": true, "kak": true, "joe": true, "ne": true, "mcedit": true, "view": true,
}

// knownEditors are looked for when VISUAL and EDITOR name nothing usable.
var knownEditors = []string{"code", "cursor", "windsurf", "zed", "subl", "codium", "code-insiders"}

// lineStyle is how an editor takes a line number.
func lineStyle(editor string, path string, line int) []string {
	l := strconv.Itoa(line)
	switch editor {
	case "code", "code-insiders", "cursor", "windsurf", "codium", "vscodium":
		return []string{"-g", path + ":" + l}
	case "zed", "subl", "sublime_text", "atom":
		return []string{path + ":" + l}
	case "idea", "goland", "webstorm", "pycharm", "phpstorm", "clion", "rubymine", "rider", "studio":
		return []string{"--line", l, path}
	case "emacsclient", "gedit", "gvim", "mvim", "kate", "geany":
		return []string{"+" + l, path}
	}
	return nil
}

func editorName(bin string) string {
	// A value of EDITOR written on another system may carry either separator.
	base := bin[strings.LastIndexAny(bin, `/\`)+1:]
	return strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(base), ".exe"), ".cmd")
}

// cmdMetachars are characters cmd.exe interprets in the arguments of a batch file, which is what most Windows editor
// launchers are (code.cmd): without a shell of ours, Windows still runs those through one.
const cmdMetachars = `&|<>^%!"()`

// editorArgv picks the editor: VISUAL, then EDITOR, then a known graphical editor, then the system's text viewer.
// lineApplied says whether the line number reached the command.
func (d *Desktop) editorArgv(path string, line int) (argv []string, lineApplied bool, err error) {
	fromEnv := func(name string) []string {
		f := strings.Fields(d.Getenv(name))
		if len(f) == 0 || terminalEditors[editorName(f[0])] {
			return nil
		}
		return f
	}
	var candidates [][]string
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := fromEnv(v); f != nil {
			candidates = append(candidates, f)
		}
	}
	for _, e := range knownEditors {
		candidates = append(candidates, []string{e})
	}
	for _, c := range candidates {
		bin, lerr := d.LookPath(c[0])
		if lerr != nil {
			continue
		}
		if d.GOOS == "windows" && strings.ContainsAny(path, cmdMetachars) {
			if e := strings.ToLower(filepath.Ext(bin)); e == ".cmd" || e == ".bat" {
				continue
			}
		}
		out := append([]string{bin}, c[1:]...)
		if line > 0 {
			if a := lineStyle(editorName(c[0]), path, line); a != nil {
				return append(out, a...), true, nil
			}
		}
		return append(out, path), false, nil
	}
	// The fallbacks open text: never `open` or start without an application, which would run an executable.
	switch d.GOOS {
	case "darwin":
		if bin, lerr := d.LookPath("open"); lerr == nil {
			return []string{bin, "-t", path}, false, nil
		}
	case "windows":
		if bin, lerr := d.LookPath("notepad.exe"); lerr == nil {
			return []string{bin, path}, false, nil
		}
	default:
		if bin, lerr := d.LookPath("xdg-open"); lerr == nil && !strings.EqualFold(filepath.Ext(path), ".desktop") {
			return []string{bin, path}, false, nil
		}
	}
	return nil, false, errors.New("no editor found on this machine: set VISUAL or EDITOR to a graphical editor (a terminal editor such as vim has no terminal to open in), or install one of " + strings.Join(knownEditors, ", "))
}

// revealArgv shows path in the platform's file manager. dir says path is a directory.
func (d *Desktop) revealArgv(path string, dir bool) ([]string, error) {
	switch d.GOOS {
	case "darwin":
		bin, err := d.LookPath("open")
		if err != nil {
			return nil, errors.New("the open command is missing on this machine")
		}
		return []string{bin, "-R", path}, nil
	case "windows":
		bin, err := d.LookPath("explorer.exe")
		if err != nil {
			return nil, errors.New("explorer.exe is missing on this machine")
		}
		if dir {
			return []string{bin, path}, nil
		}
		return []string{bin, "/select," + path}, nil
	}
	bin, err := d.LookPath("xdg-open")
	if err != nil {
		return nil, errors.New("no file manager found on this machine: xdg-open is not installed")
	}
	if !dir {
		path = filepath.Dir(path)
	}
	return []string{bin, path}, nil
}

// noDisplay says why nothing can open on a system without a graphical session.
func (d *Desktop) noDisplay() string {
	if d.GOOS == "darwin" || d.GOOS == "windows" || d.Getenv("DISPLAY") != "" || d.Getenv("WAYLAND_DISPLAY") != "" {
		return ""
	}
	return "this machine has no graphical session (DISPLAY and WAYLAND_DISPLAY are unset), so nothing can open here"
}

// inSessionScope reports whether path, with symlinks resolved, is under a working directory or the scratch directory.
func inSessionScope(env *Env, path string) bool {
	dirs := env.WorkingDirs()
	if env.ScratchDir != "" {
		dirs = append(dirs, env.ScratchDir)
	}
	return within(path, dirs)
}

func scopeHint(env *Env, input json.RawMessage, verb string) PermissionHints {
	raw := FilePath(input)
	if strings.TrimSpace(raw) == "" {
		return PermissionHints{}
	}
	path := resolvePath(env.Cwd, raw)
	if inSessionScope(env, path) {
		return PermissionHints{}
	}
	return PermissionHints{Sensitive: fmt.Sprintf("%s %s, which is outside this session's folders, on this machine.", verb, path)}
}

// deviceRefusal is why the session cannot show anything to the user on this machine.
func deviceRefusal(env *Env, d *Desktop) string {
	if h := env.Host(); h != nil {
		if o, ok := h.Interactor().(DeviceOwner); ok {
			if r := o.ElsewhereReason(); r != "" {
				return r
			}
		}
	}
	return d.noDisplay()
}

// ---- OpenInEditor ----

// OpenInEditor opens a file in the user's editor.
type OpenInEditor struct {
	// Desktop overrides the system's (tests).
	Desktop *Desktop
}

func (*OpenInEditor) Name() string                  { return "OpenInEditor" }
func (*OpenInEditor) ReadOnly(json.RawMessage) bool { return true }

// PermissionHints implements PermissionHinter: a path outside the session's scope only the user may approve.
func (*OpenInEditor) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	return scopeHint(env, call.Input, "Open")
}

func (t *OpenInEditor) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Path string `json:"path"`
		Line int    `json:"line"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return ErrorResult("InputValidationError: path is required")
	}
	if in.Line < 0 {
		return ErrorResult("InputValidationError: line numbers start at 1")
	}
	d := t.Desktop
	if d == nil {
		d = systemDesktop()
	}
	if r := deviceRefusal(env, d); r != "" {
		return ErrorResult("Cannot open an editor: %s.", r)
	}
	path := resolvePath(env.Cwd, in.Path)
	st, err := os.Stat(path)
	if err != nil {
		return ErrorResult("file does not exist: %s", path)
	}
	if st.IsDir() {
		return ErrorResult("%s is a directory; OpenInEditor opens files (RevealPath shows a directory)", path)
	}
	argv, lineApplied, err := d.editorArgv(path, in.Line)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if err := d.Start(argv); err != nil {
		return ErrorResult("could not start %s: %v", argv[0], err)
	}
	msg := fmt.Sprintf("Opened %s in %s.", path, filepath.Base(argv[0]))
	if in.Line > 0 && !lineApplied {
		msg = fmt.Sprintf("Opened %s in %s, which does not take a line number from here; the file is not at line %d.", path, filepath.Base(argv[0]), in.Line)
	}
	r := TextResult(msg, map[string]any{"path": path, "editor": filepath.Base(argv[0]), "line": in.Line})
	r.Summary = "Opened " + filepath.Base(path)
	return r
}

// ---- RevealPath ----

// RevealPath shows a file or directory in the platform's file manager.
type RevealPath struct {
	// Desktop overrides the system's (tests).
	Desktop *Desktop
}

func (*RevealPath) Name() string                  { return "RevealPath" }
func (*RevealPath) ReadOnly(json.RawMessage) bool { return true }

// PermissionHints implements PermissionHinter: a path outside the session's scope only the user may approve.
func (*RevealPath) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	return scopeHint(env, call.Input, "Show")
}

func (t *RevealPath) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return ErrorResult("InputValidationError: path is required")
	}
	d := t.Desktop
	if d == nil {
		d = systemDesktop()
	}
	if r := deviceRefusal(env, d); r != "" {
		return ErrorResult("Cannot open a file manager: %s.", r)
	}
	path := resolvePath(env.Cwd, in.Path)
	st, err := os.Stat(path)
	if err != nil {
		return ErrorResult("path does not exist: %s", path)
	}
	argv, err := d.revealArgv(path, st.IsDir())
	if err != nil {
		return ErrorResult("%v", err)
	}
	if err := d.Start(argv); err != nil {
		return ErrorResult("could not start %s: %v", argv[0], err)
	}
	r := TextResult("Showing "+path+" in the file manager.", map[string]any{"path": path})
	r.Summary = "Revealed " + filepath.Base(path)
	return r
}

// ---- KeepAwake ----

// KeepAwake turns the session's "Keep computer awake" switch on or off.
type KeepAwake struct{}

func (*KeepAwake) Name() string                  { return "KeepAwake" }
func (*KeepAwake) ReadOnly(json.RawMessage) bool { return true }

func (*KeepAwake) Run(ctx context.Context, env *Env, call Call) Result {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.Enabled == nil {
		return ErrorResult("InputValidationError: enabled is required")
	}
	c := sessionControl(env)
	if c == nil {
		return ErrorResult("KeepAwake is not available in this session")
	}
	if err := c.SetKeepAwake(*in.Enabled); err != nil {
		return ErrorResult("%v", err)
	}
	text, summary := "Sleep is held off on this machine while this session works, until you turn it off or the session closes.", "Keeping the computer awake"
	if !*in.Enabled {
		text, summary = "The computer may sleep again as usual.", "Sleep allowed again"
	}
	c.NoteChange("keep_awake", summary+" (changed by the agent).")
	r := TextResult(text, map[string]any{"enabled": *in.Enabled})
	r.Summary = summary
	return r
}
