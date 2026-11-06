package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const runNotebook = `{
 "cells": [
  {
   "cell_type": "code",
   "execution_count": null,
   "id": "a1",
   "metadata": {"custom": true},
   "outputs": [],
   "source": ["x = 1\n"]
  },
  {
   "cell_type": "markdown",
   "id": "m1",
   "metadata": {},
   "source": ["# Title"]
  },
  {
   "cell_type": "code",
   "execution_count": null,
   "id": "b2",
   "metadata": {},
   "outputs": [],
   "source": ["print(x + 1)\n"]
  }
 ],
 "metadata": {"kernelspec": {"name": "python3", "language": "python"}},
 "nbformat": 4,
 "nbformat_minor": 5
}
`

// fakePython writes a script that answers as the driver does: argv is run.py, the notebook, the last cell, the output file.
func fakePython(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for python")
	}
	p := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const fakeRunOK = `cat > "$4" <<'JSON'
{"cells":[{"index":0,"execution_count":1,"outputs":[]},{"index":2,"execution_count":2,"outputs":[{"output_type":"stream","name":"stdout","text":["2\n"]}]}],"failed":""}
JSON
`

func TestNotebookRunWritesOutputsAndNothingElse(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, runNotebook)
	python := fakePython(t, fakeRunOK)
	r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": python, "description": "run"})
	if r.IsError {
		t.Fatalf("%s", text(r))
	}
	if !strings.Contains(text(r), "Ran 2 code cells") || !strings.Contains(text(r), `<output type="stream" name="stdout">`) || !strings.Contains(text(r), "2\n") {
		t.Fatalf("%s", text(r))
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var nb struct {
		Cells []struct {
			ExecutionCount *int              `json:"execution_count"`
			Outputs        []json.RawMessage `json:"outputs"`
			Metadata       map[string]any    `json:"metadata"`
		} `json:"cells"`
		NBFormat int `json:"nbformat"`
	}
	if err := json.Unmarshal(got, &nb); err != nil {
		t.Fatalf("not JSON any more: %v\n%s", err, got)
	}
	if *nb.Cells[0].ExecutionCount != 1 || *nb.Cells[2].ExecutionCount != 2 || len(nb.Cells[2].Outputs) != 1 {
		t.Fatalf("%s", got)
	}
	// The markdown cell, metadata and everything around the cells stay byte for byte.
	if !strings.Contains(string(got), `"metadata": {"custom": true}`) || !strings.Contains(string(got), `"source": ["# Title"]`) || !strings.HasSuffix(string(got), "\"nbformat_minor\": 5\n}\n") {
		t.Fatalf("%s", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode())
	}
	// What was written counts as read, so an edit can follow.
	if msg := env.Files.CheckWritable(p); msg != "" {
		t.Fatal(msg)
	}
}

func TestNotebookRunThroughACell(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, runNotebook)
	python := fakePython(t, `echo "$3" > "$(dirname "$4")/../upto"
cat > "$4" <<'JSON'
{"cells":[{"index":0,"execution_count":1,"outputs":[]}],"failed":""}
JSON
`)
	r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": python, "cell_id": "a1", "description": "run"})
	if r.IsError {
		t.Fatalf("%s", text(r))
	}
	got, err := os.ReadFile(filepath.Join(env.ScratchDir, "upto"))
	if err != nil || strings.TrimSpace(string(got)) != "0" {
		t.Fatalf("%q %v", got, err)
	}
	if r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": python, "cell_id": "nope", "description": "run"}); !r.IsError || !strings.Contains(text(r), "a1") {
		t.Fatalf("%s", text(r))
	}
	if r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": python, "cell_id": "a1", "cell_index": 0, "description": "run"}); !r.IsError {
		t.Fatal("both addresses")
	}
}

func TestNotebookRunFailures(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, runNotebook)

	// A cell that fails: outputs up to it are written, and the call is an error.
	failing := fakePython(t, `cat > "$4" <<'JSON'
{"cells":[{"index":0,"execution_count":1,"outputs":[{"output_type":"error","ename":"ZeroDivisionError","evalue":"division by zero","traceback":["bad"]}]}],"failed":"ZeroDivisionError: division by zero"}
JSON
`)
	r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": failing, "description": "run"})
	if !r.IsError || !strings.Contains(text(r), "ZeroDivisionError") {
		t.Fatalf("%s", text(r))
	}
	if got, _ := os.ReadFile(p); !strings.Contains(string(got), "division by zero") {
		t.Fatalf("the traceback belongs in the file: %s", got)
	}

	// Jupyter is not there: says what is missing, writes nothing.
	p = writeNotebook(t, env, runNotebook)
	missing := fakePython(t, `echo "No module named nbclient" >&2; exit 42`)
	r = call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": missing, "description": "run"})
	if !r.IsError || !strings.Contains(text(r), "nbclient") || !strings.Contains(text(r), "pip install") {
		t.Fatalf("%s", text(r))
	}
	if got, _ := os.ReadFile(p); string(got) != runNotebook {
		t.Fatalf("changed: %s", got)
	}

	// Any other crash.
	crash := fakePython(t, `echo boom >&2; exit 3`)
	r = call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": crash, "description": "run"})
	if !r.IsError || !strings.Contains(text(r), "boom") {
		t.Fatalf("%s", text(r))
	}

	// The user edited the file while it ran: their edit stays.
	edit := fakePython(t, `printf '{"cells":[],"nbformat":4,"nbformat_minor":5,"metadata":{}}' > "$2"
`+fakeRunOK)
	r = call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": edit, "description": "run"})
	if !r.IsError || !strings.Contains(text(r), "changed") {
		t.Fatalf("%s", text(r))
	}
	if got, _ := os.ReadFile(p); strings.Contains(string(got), "execution_count") {
		t.Fatalf("overwrote the edit: %s", got)
	}

	// Out of scope, not a notebook, no such file.
	outside := filepath.Join(t.TempDir(), "o.ipynb")
	if err := os.WriteFile(outside, []byte(runNotebook), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"outside": outside, "not a notebook": filepath.Join(env.Cwd, "a.txt"), "missing": filepath.Join(env.Cwd, "none.ipynb"), "relative": "nb.ipynb"} {
		if r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": path, "python": crash, "description": "run"}); !r.IsError {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestNotebookRunIsCheckedAsACommand(t *testing.T) {
	env := newEnv(t)
	raw, _ := json.Marshal(map[string]any{"notebook_path": "/w/a b.ipynb", "run_outside_sandbox": true})
	h := (&NotebookRun{}).PermissionHints(context.Background(), env, Call{Input: raw})
	if h.CheckAs != "Bash" || !h.OutsideSandbox || !strings.Contains(h.Command, "python3") || !strings.Contains(h.Command, "'/w/a b.ipynb'") {
		t.Fatalf("%+v", h)
	}
	if (&NotebookRun{}).ReadOnly(raw) {
		t.Fatal("running code is not read-only")
	}
	venv := filepath.Join(env.Cwd, ".venv", "bin")
	if err := os.MkdirAll(venv, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "python"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := pythonFor(env, ""); got != filepath.Join(venv, "python") {
		t.Fatalf("%s", got)
	}
}

// The real thing, when this machine has it: the driver with a real kernel.
func TestNotebookRunWithRealJupyter(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	if err := exec.Command(python, "-c", "import nbclient, nbformat, ipykernel").Run(); err != nil {
		t.Skip("no Jupyter packages")
	}
	env := newEnv(t)
	p := writeNotebook(t, env, runNotebook)
	r := call(t, env, &NotebookRun{}, map[string]any{"notebook_path": p, "python": python, "description": "run"})
	if r.IsError {
		t.Fatalf("%s", text(r))
	}
	if !regexp.MustCompile(`(?s)<output type="stream".*2`).MatchString(text(r)) {
		t.Fatalf("%s", text(r))
	}
}
