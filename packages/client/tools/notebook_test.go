package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// As nbformat writes it: one space of indent, sorted keys, a final newline. Metadata in an odd layout on purpose.
const sampleNotebook = `{
 "cells": [
  {
   "cell_type": "markdown",
   "id": "intro",
   "metadata": {},
   "source": [
    "# Title\n",
    "text <b>"
   ]
  },
  {
   "cell_type": "code",
   "execution_count": 7,
   "id": "calc",
   "metadata": {"tags": ["keep"],   "collapsed": true},
   "outputs": [
    {
     "name": "stdout",
     "output_type": "stream",
     "text": [
      "41\n"
     ]
    },
    {
     "data": {"image/png": "aGVsbG8=", "text/plain": ["<Figure>"]},
     "metadata": {},
     "output_type": "display_data"
    },
    {
     "ename": "ValueError",
     "evalue": "bad",
     "output_type": "error",
     "traceback": ["\u001b[31mValueError\u001b[0m: bad"]
    }
   ],
   "source": [
    "x = 41\n",
    "print(x)"
   ]
  }
 ],
 "metadata": {
  "kernelspec": {"display_name": "Python 3", "language": "python", "name": "python3"}
 },
 "nbformat": 4,
 "nbformat_minor": 5
}
`

func writeNotebook(t *testing.T, env *Env, content string) string {
	t.Helper()
	p := filepath.Join(env.Cwd, "nb.ipynb")
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func readBack(t *testing.T, env *Env, p string) string {
	t.Helper()
	if r := call(t, env, &Read{}, map[string]any{"file_path": p}); r.IsError {
		t.Fatalf("read: %s", text(r))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func nbEdit(t *testing.T, env *Env, in map[string]any) Result {
	t.Helper()
	r := call(t, env, &NotebookEdit{}, in)
	if r.IsError {
		t.Fatalf("edit: %s", text(r))
	}
	return r
}

func TestReadNotebook(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	r := call(t, env, &Read{}, map[string]any{"file_path": p})
	out := text(r)
	for _, want := range []string{
		"nbformat 4.5, 2 cells, kernel python",
		`<cell index="0" id="intro" type="markdown">`, "# Title\ntext <b>\n</cell>",
		`<cell index="1" id="calc" type="code" execution_count="7">`, "x = 41\nprint(x)\n",
		`<output type="stream" name="stdout">` + "\n41\n</output>",
		"<Figure>\n[image/png output", "ValueError: bad",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "aGVsbG8") {
		t.Errorf("escape codes or image data in:\n%s", out)
	}
	if r.Summary != "Read 2 cells" {
		t.Errorf("summary %q", r.Summary)
	}
	if r := call(t, env, &Read{}, map[string]any{"file_path": p, "offset": 2, "limit": 1}); strings.Contains(text(r), "intro") || !strings.Contains(text(r), "calc") {
		t.Errorf("offset counts cells: %s", text(r))
	}
	if !env.Files.WasRead(p) {
		t.Error("reading a notebook satisfies read-before-edit")
	}
}

func TestReadBrokenNotebookIsText(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, "{ not json")
	if r := call(t, env, &Read{}, map[string]any{"file_path": p}); r.IsError || !strings.Contains(text(r), "{ not json") {
		t.Fatalf("%q", text(r))
	}
}

func TestNotebookEditNeedsRead(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	src := "y"
	in := map[string]any{"notebook_path": p, "cell_id": "calc", "new_source": src}
	if r := call(t, env, &NotebookEdit{}, in); !r.IsError || !strings.Contains(text(r), "not been read") {
		t.Fatalf("unread: %q", text(r))
	}
	readBack(t, env, p)
	if err := os.WriteFile(p, []byte(sampleNotebook+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if r := call(t, env, &NotebookEdit{}, in); !r.IsError || !strings.Contains(text(r), "modified since read") {
		t.Fatalf("changed since read: %q", text(r))
	}
	if data, _ := os.ReadFile(p); string(data) != sampleNotebook+"\n" {
		t.Fatal("a refused edit must not write")
	}
}

func TestNotebookEditReplaceCodeCell(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	readBack(t, env, p)
	var tracked []string
	env.BeforeEdit = func(path string) { tracked = append(tracked, path) }
	r := nbEdit(t, env, map[string]any{"notebook_path": p, "cell_id": "calc", "new_source": "x = 42\nprint(x)\n<tag> & more\n"})
	if len(tracked) != 1 || tracked[0] != p {
		t.Fatalf("file history: %v", tracked)
	}
	data, _ := os.ReadFile(p)
	// Everything but the cell's outputs, execution count and source stays byte for byte.
	head := sampleNotebook[:strings.Index(sampleNotebook, `"execution_count": 7`)]
	tail := sampleNotebook[strings.Index(sampleNotebook, "\n  }\n ],\n \"metadata\""):]
	want := head + `"execution_count": null,
   "id": "calc",
   "metadata": {"tags": ["keep"],   "collapsed": true},
   "outputs": [],
   "source": [
    "x = 42\n",
    "print(x)\n",
    "<tag> & more\n"
   ]` + tail
	if string(data) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", data, want)
	}
	sm := r.Structured.(map[string]any)
	if sm["filePath"] != p || sm["cellId"] != "calc" || sm["editMode"] != "replace" {
		t.Fatalf("structured %+v", sm)
	}
	add, del := DiffStats(sm["structuredPatch"].([]Hunk))
	if add != 2 || del != 1 {
		t.Fatalf("patch +%d -%d", add, del)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	// The edit counts as read: a second one needs no Read.
	nbEdit(t, env, map[string]any{"notebook_path": p, "cell_index": 0, "new_source": "# Other"})
}

func TestNotebookEditMarkdownKeepsNoOutputs(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	readBack(t, env, p)
	nbEdit(t, env, map[string]any{"notebook_path": p, "cell_index": 0, "new_source": "# New\n"})
	var nb struct {
		Cells []map[string]any `json:"cells"`
	}
	data, _ := os.ReadFile(p)
	if err := json.Unmarshal(data, &nb); err != nil {
		t.Fatal(err)
	}
	if _, ok := nb.Cells[0]["outputs"]; ok {
		t.Fatalf("markdown cell got outputs: %+v", nb.Cells[0])
	}
	if nb.Cells[1]["execution_count"] != float64(7) {
		t.Fatalf("other cell touched: %+v", nb.Cells[1])
	}
}

func TestNotebookEditChangesCellType(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	readBack(t, env, p)
	nbEdit(t, env, map[string]any{"notebook_path": p, "cell_id": "calc", "cell_type": "markdown"})
	cell := func(i int) map[string]any {
		var nb struct {
			Cells []map[string]any `json:"cells"`
		}
		data, _ := os.ReadFile(p)
		if err := json.Unmarshal(data, &nb); err != nil {
			t.Fatal(err)
		}
		return nb.Cells[i]
	}
	c := cell(1)
	_, hasOut := c["outputs"]
	_, hasCount := c["execution_count"]
	if c["cell_type"] != "markdown" || hasOut || hasCount || len(c["source"].([]any)) != 2 {
		t.Fatalf("to markdown: %+v", c)
	}
	nbEdit(t, env, map[string]any{"notebook_path": p, "cell_id": "intro", "cell_type": "code", "new_source": "1+1"})
	c = cell(0)
	if c["cell_type"] != "code" || len(c["outputs"].([]any)) != 0 || c["execution_count"] != nil {
		t.Fatalf("to code: %+v", c)
	}
	if r := call(t, env, &NotebookEdit{}, map[string]any{"notebook_path": p, "cell_id": "intro", "cell_type": "code", "new_source": "1+1"}); !r.IsError {
		t.Fatal("an edit that changes nothing is an error, as in Edit")
	}
}

func TestNotebookEditInsert(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	readBack(t, env, p)
	r := nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "insert", "cell_id": "intro", "cell_type": "code", "new_source": "a\nb"})
	id, _ := r.Structured.(map[string]any)["cellId"].(string)
	if len(id) != 8 {
		t.Fatalf("new cell id %q", id)
	}
	data, _ := os.ReadFile(p)
	want := `  },
  {
   "cell_type": "code",
   "execution_count": null,
   "id": "` + id + `",
   "metadata": {},
   "outputs": [],
   "source": [
    "a\n",
    "b"
   ]
  },
  {
   "cell_type": "code",
   "execution_count": 7,`
	if !strings.Contains(string(data), want) {
		t.Fatalf("got:\n%s", data)
	}
	if r.Structured.(map[string]any)["cellIndex"] != 1 {
		t.Fatalf("index %+v", r.Structured)
	}
	// Without an address the cell goes first.
	nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "insert", "cell_type": "markdown", "new_source": "first"})
	doc, err := parseNotebook(mustRead(t, p))
	if err != nil || len(doc.cells) != 4 || doc.cells[0].source != "first" || doc.cells[2].source != "a\nb" {
		t.Fatalf("doc %+v %v", doc, err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNotebookEditDeleteRestoresLayout(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	before := readBack(t, env, p)
	r := nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "insert", "cell_id": "calc", "cell_type": "raw", "new_source": "z"})
	id := r.Structured.(map[string]any)["cellId"].(string)
	nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "delete", "cell_id": id})
	if got := string(mustRead(t, p)); got != before {
		t.Fatalf("insert then delete must restore the file:\n%s", got)
	}
	// Delete the first and then the only cell.
	nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "delete", "cell_index": 0})
	if doc, err := parseNotebook(mustRead(t, p)); err != nil || len(doc.cells) != 1 || doc.cells[0].id != "calc" {
		t.Fatalf("after delete: %+v %v", doc, err)
	}
	nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "delete", "cell_id": "calc"})
	data := mustRead(t, p)
	if !strings.Contains(string(data), `"cells": [],`) {
		t.Fatalf("empty:\n%s", data)
	}
	// Into the empty array, in the document's layout.
	nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "insert", "cell_type": "markdown", "new_source": "back\n"})
	data = mustRead(t, p)
	if !strings.Contains(string(data), "\"cells\": [\n  {\n   \"cell_type\": \"markdown\",") || !strings.Contains(string(data), "    \"back\\n\"\n   ]\n  }\n ],") {
		t.Fatalf("insert into empty:\n%s", data)
	}
}

func TestNotebookEditOlderFormatsAndStyles(t *testing.T) {
	env := newEnv(t)
	// nbformat 4.4: no ids, addressed by index, no id invented. Compact JSON, source as a string.
	compact := `{"cells":[{"cell_type":"code","execution_count":1,"metadata":{},"outputs":[{"output_type":"stream","name":"stdout","text":"1"}],"source":"print(1)"}],"metadata":{},"nbformat":4,"nbformat_minor":4}`
	p := writeNotebook(t, env, compact)
	readBack(t, env, p)
	nbEdit(t, env, map[string]any{"notebook_path": p, "cell_index": 0, "new_source": "print(2)\nprint(3)"})
	if got := string(mustRead(t, p)); got != `{"cells":[{"cell_type":"code","execution_count":null,"metadata":{},"outputs":[],"source":"print(2)\nprint(3)"}],"metadata":{},"nbformat":4,"nbformat_minor":4}` {
		t.Fatalf("compact: %s", got)
	}
	nbEdit(t, env, map[string]any{"notebook_path": p, "edit_mode": "insert", "cell_index": 0, "cell_type": "code", "new_source": "x"})
	if got := string(mustRead(t, p)); !strings.Contains(got, `,{"cell_type":"code","execution_count":null,"metadata":{},"outputs":[],"source":["x"]}]`) {
		t.Fatalf("no id in nbformat 4.4: %s", got)
	}
}

func TestNotebookEditCRLFAndTwoSpaces(t *testing.T) {
	env := newEnv(t)
	nb := strings.ReplaceAll(`{
  "cells": [
    {
      "cell_type": "markdown",
      "metadata": {},
      "source": []
    }
  ],
  "nbformat": 4,
  "nbformat_minor": 2
}
`, "\n", "\r\n")
	p := writeNotebook(t, env, nb)
	readBack(t, env, p)
	nbEdit(t, env, map[string]any{"notebook_path": p, "cell_index": 0, "new_source": "a\nb"})
	want := strings.Replace(nb, `"source": []`, "\"source\": [\r\n        \"a\\n\",\r\n        \"b\"\r\n      ]", 1)
	if got := string(mustRead(t, p)); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestNotebookEditErrors(t *testing.T) {
	env := newEnv(t)
	p := writeNotebook(t, env, sampleNotebook)
	readBack(t, env, p)
	txt := filepath.Join(env.Cwd, "a.txt")
	for name, in := range map[string]map[string]any{
		"relative":   {"notebook_path": "nb.ipynb", "cell_index": 0, "new_source": "x"},
		"not ipynb":  {"notebook_path": txt, "cell_index": 0, "new_source": "x"},
		"no address": {"notebook_path": p, "new_source": "x"},
		"both":       {"notebook_path": p, "cell_id": "calc", "cell_index": 0, "new_source": "x"},
		"no id":      {"notebook_path": p, "cell_id": "nope", "new_source": "x"},
		"range":      {"notebook_path": p, "cell_index": 5, "new_source": "x"},
		"negative":   {"notebook_path": p, "cell_index": -1, "new_source": "x"},
		"nothing":    {"notebook_path": p, "cell_index": 0},
		"type":       {"notebook_path": p, "cell_index": 0, "cell_type": "sql"},
		"mode":       {"notebook_path": p, "cell_index": 0, "edit_mode": "move"},
		"insert":     {"notebook_path": p, "edit_mode": "insert", "new_source": "x"},
		"missing":    {"notebook_path": filepath.Join(env.Cwd, "none.ipynb"), "cell_index": 0, "new_source": "x"},
	} {
		if r := call(t, env, &NotebookEdit{}, in); !r.IsError {
			t.Errorf("%s: want an error", name)
		}
	}
	if string(mustRead(t, p)) != sampleNotebook {
		t.Fatal("failed edits must not write")
	}
	if FilePath(json.RawMessage(`{"notebook_path":"/a/b.ipynb"}`)) != "/a/b.ipynb" {
		t.Fatal("the permission check must see the notebook's path")
	}
}
