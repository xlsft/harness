package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// showIA is an Interactor that records Show calls. The embedded interface
// stays nil: the Show tool must not call anything else.
type showIA struct {
	Interactor
	status ShowStatus
	ids    []string
	got    []ShowTarget
}

func (s *showIA) Show(id string, t ShowTarget) ShowStatus {
	s.ids = append(s.ids, id)
	s.got = append(s.got, t)
	return s.status
}

func TestShowValidation(t *testing.T) {
	env := newEnv(t)
	write(t, filepath.Join(env.Cwd, "src", "x.go"), "a\nb\nc\nd\n")
	outside := filepath.Join(t.TempDir(), "o.txt")
	write(t, outside, "x\n")
	env.SetHost(&fakeHost{ia: &showIA{status: ShowOpened}})
	for _, tc := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{}, "target must be"},
		{map[string]any{"target": "folder"}, "target must be"},
		{map[string]any{"target": "file"}, "path is required"},
		{map[string]any{"target": "file", "path": "missing.go"}, "does not exist"},
		{map[string]any{"target": "file", "path": "src"}, "is a directory"},
		{map[string]any{"target": "file", "path": outside}, "outside the working directories"},
		{map[string]any{"target": "file", "path": "src/x.go", "line": 9}, "past the end of the file (4 lines)"},
		{map[string]any{"target": "file", "path": "src/x.go", "line": 3, "end_line": 2}, "is before line"},
		{map[string]any{"target": "file", "path": "src/x.go", "end_line": 2}, "end_line needs line"},
		{map[string]any{"target": "file", "path": "src/x.go", "line": -1}, "start at 1"},
		{map[string]any{"target": "diff", "path": outside}, "outside the working directories"},
		{map[string]any{"target": "diff", "line": 3}, "target \"file\" only"},
		{map[string]any{"target": "url", "url": "file:///etc/passwd"}, "scheme must be http or https"},
		{map[string]any{"target": "url", "url": "javascript:alert(1)"}, "scheme must be http or https"},
		{map[string]any{"target": "url", "url": "http://"}, "must include a host"},
		{map[string]any{"target": "url"}, "absolute http or https URL"},
		{map[string]any{"target": "file", "path": "src/x.go", "url": "http://x"}, "target \"url\" only"},
	} {
		r := call(t, env, &Show{}, tc.in)
		if !r.IsError || !strings.Contains(text(r), tc.want) {
			t.Errorf("%v: want error %q, got %q", tc.in, tc.want, text(r))
		}
	}
}

func TestShowEmitsRequest(t *testing.T) {
	env := newEnv(t)
	p := filepath.Join(env.Cwd, "src", "x.go")
	write(t, p, "a\nb\nc\nd")
	ia := &showIA{status: ShowOpened}
	env.SetHost(&fakeHost{ia: ia})

	r := call(t, env, &Show{}, map[string]any{"target": "file", "path": "src/x.go", "line": 2, "end_line": 40})
	if r.IsError || text(r) != "Opened src/x.go:2-4 in the Files pane." || r.Summary != "Opened src/x.go:2-4" {
		t.Fatalf("file: %q / %q", text(r), r.Summary)
	}
	if got := ia.got[0]; got.Kind != ShowFile || got.Path != p || got.LineStart != 2 || got.LineEnd != 4 || ia.ids[0] != "toolu_test" {
		t.Fatalf("file target: %+v %v", got, ia.ids)
	}
	if m := r.Structured.(map[string]any); m["shown"] != true || m["path"] != p || m["line"] != 2 {
		t.Fatalf("structured: %v", m)
	}

	r = call(t, env, &Show{}, map[string]any{"target": "diff", "path": "src/gone.go"})
	if r.IsError || text(r) != "Opened the diff of src/gone.go in the Diff pane." {
		t.Fatalf("diff: %q", text(r))
	}
	r = call(t, env, &Show{}, map[string]any{"target": "diff"})
	if r.IsError || text(r) != "Opened the session diff in the Diff pane." || ia.got[2].Path != "" {
		t.Fatalf("whole diff: %q %+v", text(r), ia.got[2])
	}

	ia.status = ShowPending
	r = call(t, env, &Show{}, map[string]any{"target": "url", "url": "http://localhost:3000/login"})
	if r.IsError || !strings.Contains(text(r), "Requested http://localhost:3000/login in the Browser pane. Nobody is viewing") {
		t.Fatalf("url: %q", text(r))
	}
	if got := ia.got[3]; got.Kind != ShowURL || got.URL != "http://localhost:3000/login" {
		t.Fatalf("url target: %+v", got)
	}
}

func TestShowWithoutUI(t *testing.T) {
	env := newEnv(t)
	write(t, filepath.Join(env.Cwd, "a.go"), "x\n")
	for _, h := range []Host{nil, &fakeHost{}, &fakeHost{ia: &fakeIA{}}, &fakeHost{ia: &showIA{status: ShowUnavailable}}} {
		env.SetHost(h)
		r := call(t, env, &Show{}, map[string]any{"target": "file", "path": "a.go", "line": 1})
		if r.IsError || !strings.HasPrefix(text(r), "Nothing was shown: no UI that can open it is attached") || !strings.Contains(text(r), "a.go:1") {
			t.Fatalf("no UI: %q", text(r))
		}
		if m := r.Structured.(map[string]any); m["shown"] != false {
			t.Fatalf("structured: %v", m)
		}
	}
}
