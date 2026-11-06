package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// NotebookRun limits.
const (
	notebookRunDefaultTimeout = 5 * time.Minute
	notebookRunMaxTimeout     = 30 * time.Minute
)

// notebookDriver runs a notebook with the user's own Jupyter packages and writes what the cells produced to a JSON file.
// A failing cell stops the run, as "Run All" does; what ran until then is kept.
const notebookDriver = `import json, sys
try:
    import nbformat
    from nbclient import NotebookClient
    from nbclient.exceptions import CellExecutionError
except ImportError as e:
    print("jupyter packages are missing: %s" % e, file=sys.stderr)
    sys.exit(42)
path, upto, out = sys.argv[1], int(sys.argv[2]), sys.argv[3]
nb = nbformat.read(path, as_version=4)
if upto >= 0:
    nb.cells = nb.cells[: upto + 1]
failed = ""
try:
    NotebookClient(nb, timeout=None, allow_errors=False).execute()
except CellExecutionError as e:
    failed = "%s: %s" % (e.ename, e.evalue)
cells = [{"index": i, "execution_count": c.get("execution_count"), "outputs": c.get("outputs", [])} for i, c in enumerate(nb.cells) if c.cell_type == "code"]
json.dump({"cells": cells, "failed": failed}, open(out, "w"))
`

// NotebookRun executes the code cells of a notebook, in a fresh kernel, and writes their outputs back into the file. It
// is arbitrary code: the permission check treats it as the shell command it is, and it runs under the same sandbox.
// Only the outputs change: the rest of the file stays byte for byte what it was.
type NotebookRun struct{}

type notebookRunInput struct {
	NotebookPath string `json:"notebook_path"`
	CellID       string `json:"cell_id"`
	CellIndex    *int   `json:"cell_index"`
	Python       string `json:"python"`
	Timeout      *int   `json:"timeout"`
	// RunOutsideSandbox: see Bash's.
	RunOutsideSandbox bool `json:"run_outside_sandbox"`
}

func (*NotebookRun) Name() string                  { return "NotebookRun" }
func (*NotebookRun) ReadOnly(json.RawMessage) bool { return false }

// PermissionHints implements PermissionHinter: running a notebook is checked as running its interpreter.
func (*NotebookRun) PermissionHints(_ context.Context, env *Env, call Call) PermissionHints {
	var in notebookRunInput
	if json.Unmarshal(call.Input, &in) != nil {
		return PermissionHints{}
	}
	return PermissionHints{CheckAs: "Bash", Command: pythonFor(env, in.Python) + " -m nbclient " + shellQuote(in.NotebookPath), OutsideSandbox: in.RunOutsideSandbox, Label: "NotebookRun"}
}

// pythonFor picks the interpreter: the one asked for, the project's virtual environment, else python3 from the PATH.
func pythonFor(env *Env, asked string) string {
	if asked != "" {
		return asked
	}
	for _, dir := range []string{".venv", "venv"} {
		p := filepath.Join(env.CurrentDir(), dir, "bin", "python")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return "python3"
}

type notebookRunResult struct {
	Cells []struct {
		Index          int             `json:"index"`
		ExecutionCount json.RawMessage `json:"execution_count"`
		Outputs        json.RawMessage `json:"outputs"`
	} `json:"cells"`
	Failed string `json:"failed"`
}

// spliceOutputs writes each cell's outputs and execution count into its bytes, last cell first so offsets stay valid.
func (d *nbDoc) spliceOutputs(res *notebookRunResult) ([]byte, error) {
	data := d.data
	for i := len(res.Cells) - 1; i >= 0; i-- {
		rc := res.Cells[i]
		if rc.Index < 0 || rc.Index >= len(d.cells) {
			return nil, fmt.Errorf("the run reported a cell that does not exist (%d)", rc.Index)
		}
		c := d.cells[rc.Index]
		if c.cellType != "code" {
			return nil, fmt.Errorf("the run reported outputs for cell %d, which is not a code cell", rc.Index)
		}
		o, err := parseObject(d.data[c.start:c.end])
		if err != nil {
			return nil, err
		}
		outputs := rc.Outputs
		if len(outputs) == 0 {
			outputs = json.RawMessage("[]")
		}
		o.set("outputs", outputs)
		count := rc.ExecutionCount
		if len(count) == 0 {
			count = json.RawMessage("null")
		}
		o.set("execution_count", count)
		data = append(append(append([]byte(nil), data[:c.start]...), o.render()...), data[c.end:]...)
	}
	return data, nil
}

func (*NotebookRun) Run(ctx context.Context, env *Env, call Call) Result {
	var in notebookRunInput
	if err := decode(call.Input, &in); err != nil {
		return ErrorResult("%v", err)
	}
	if in.NotebookPath == "" {
		return ErrorResult("InputValidationError: notebook_path is required")
	}
	if !filepath.IsAbs(in.NotebookPath) {
		return ErrorResult("notebook_path must be an absolute path, got %q", in.NotebookPath)
	}
	if !strings.EqualFold(filepath.Ext(in.NotebookPath), ".ipynb") {
		return ErrorResult("%s is not a notebook (.ipynb).", in.NotebookPath)
	}
	if in.CellID != "" && in.CellIndex != nil {
		return ErrorResult("InputValidationError: pass either cell_id or cell_index, not both")
	}
	path := filepath.Clean(in.NotebookPath)
	scope := env.WorkingDirs()
	if env.ScratchDir != "" {
		scope = append(scope, env.ScratchDir)
	}
	if !within(path, scope) {
		return ErrorResult("%s is outside the session's working directories.", path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrorResult("File does not exist. Current working directory: %s", env.CurrentDir())
	}
	if err != nil {
		return ErrorResult("%v", err)
	}
	doc, err := parseNotebook(data)
	if err != nil {
		return ErrorResult("%s is not a valid notebook: %v", path, err)
	}
	upto := -1
	if in.CellID != "" || in.CellIndex != nil {
		if upto, err = doc.find(in.CellID, in.CellIndex); err != nil {
			return ErrorResult("%v", err)
		}
	}
	timeout := notebookRunDefaultTimeout
	if in.Timeout != nil && *in.Timeout > 0 {
		timeout = min(time.Duration(*in.Timeout)*time.Millisecond, notebookRunMaxTimeout)
	}
	if env.ScratchDir == "" {
		return ErrorResult("This session has no scratch directory to run notebooks from.")
	}
	if err := os.MkdirAll(env.ScratchDir, 0o700); err != nil {
		return ErrorResult("%v", err)
	}
	dir, err := os.MkdirTemp(env.ScratchDir, "notebook-*")
	if err != nil {
		return ErrorResult("%v", err)
	}
	defer os.RemoveAll(dir)
	driver := filepath.Join(dir, "run.py")
	if err := os.WriteFile(driver, []byte(notebookDriver), 0o600); err != nil {
		return ErrorResult("%v", err)
	}
	out := filepath.Join(dir, "out.json")
	python := pythonFor(env, in.Python)
	// The kernel's connection file goes here too: the sandbox lets it write nowhere else.
	command := fmt.Sprintf("JUPYTER_RUNTIME_DIR=%s %s %s %s %d %s", shellQuote(dir), shellQuote(python), shellQuote(driver), shellQuote(path), upto, shellQuote(out))
	res, err := env.Shell.Run(ctx, command, ExecOptions{Timeout: timeout, ToolUseID: call.ID, Confine: !in.RunOutsideSandbox})
	if err != nil {
		return ErrorResult("failed to start the notebook: %v", err)
	}
	if res.Interrupted {
		return ErrorResult("[Notebook run interrupted]")
	}
	if res.TimedOut {
		return ErrorResult("The notebook did not finish within %s. Nothing was written. Pass a longer timeout, or run fewer cells with cell_id.", timeout)
	}
	if res.ExitCode == 42 {
		return ErrorResult("%s has no Jupyter packages (%s). Running a notebook needs nbclient, nbformat and ipykernel in that environment: `%s -m pip install nbclient nbformat ipykernel`, or pass python for another interpreter. Ask the user before installing anything.", python, strings.TrimSpace(res.Stderr), python)
	}
	raw, readErr := os.ReadFile(out)
	if res.ExitCode != 0 || readErr != nil {
		return ErrorResult("The notebook could not run (exit code %d):\n%s", res.ExitCode, headTail(strings.TrimSpace(res.Stderr+"\n"+res.Stdout), notebookOutputChars))
	}
	var run notebookRunResult
	if err := json.Unmarshal(raw, &run); err != nil {
		return ErrorResult("The notebook's run gave an unreadable result: %v", err)
	}
	// The kernel may be slow and the user may have edited the file meanwhile: their edit wins.
	if now, err := os.ReadFile(path); err != nil || string(now) != string(data) {
		return ErrorResult("%s changed while its cells ran, so the outputs were not written. Run it again.", path)
	}
	env.beforeEdit(path)
	updated, err := doc.spliceOutputs(&run)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if err := writeFileKeepingMode(path, updated); err != nil {
		return ErrorResult("%v", err)
	}
	env.Files.MarkReadContent(path, updated)
	env.afterEdit(ctx, path)

	var nb struct {
		Cells []nbReadCell `json:"cells"`
	}
	var b strings.Builder
	if json.Unmarshal(updated, &nb) == nil {
		fmt.Fprintf(&b, "Ran %d code cells of %s.\n", len(run.Cells), path)
		if run.Failed != "" {
			fmt.Fprintf(&b, "A cell failed and stopped the run: %s\n", ansiStrip(run.Failed))
		}
		for _, rc := range run.Cells {
			if rc.Index < len(nb.Cells) {
				nb.Cells[rc.Index].render(&b, rc.Index)
			}
		}
	}
	text := b.String()
	if len(text) > ReadMaxOutputBytes {
		text = headTail(text, ReadMaxOutputBytes)
	}
	r := TextResult(text, map[string]any{"type": "notebook_run", "filePath": path, "cellsRun": len(run.Cells), "failed": run.Failed})
	r.IsError = run.Failed != ""
	r.Summary = fmt.Sprintf("Ran %d cells", len(run.Cells))
	return r
}

var ansiStripRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func ansiStrip(s string) string { return ansiStripRe.ReplaceAllString(s, "") }

func writeFileKeepingMode(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".notebook-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
