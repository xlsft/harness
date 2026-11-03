package tools

import (
	"strings"
	"testing"
)

type htmlIA struct {
	Interactor
	renders bool
}

func (h *htmlIA) RendersHTML() bool { return h.renders }

func TestVisualValidation(t *testing.T) {
	env := newEnv(t)
	env.SetHost(&fakeHost{ia: &htmlIA{renders: true}})
	for _, tc := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"title": "x", "html": "<p>"}, "id must be"},
		{map[string]any{"id": "Load Chart", "title": "x", "html": "<p>"}, "id must be"},
		{map[string]any{"id": "load", "html": "<p>"}, "title is required"},
		{map[string]any{"id": "load", "title": strings.Repeat("t", 81), "html": "<p>"}, "longer than 80"},
		{map[string]any{"id": "load", "title": "Load", "html": "  "}, "html is required"},
		{map[string]any{"id": "load", "title": "Load", "html": strings.Repeat("x", maxVisualHTML+1)}, "the limit is 512 KB"},
	} {
		r := call(t, env, &Visual{}, tc.in)
		if !r.IsError || !strings.Contains(text(r), tc.want) {
			t.Errorf("%v: want error %q, got %q", tc.in, tc.want, text(r))
		}
	}
}

func TestVisualShown(t *testing.T) {
	env := newEnv(t)
	env.SetHost(&fakeHost{ia: &htmlIA{renders: true}})
	r := call(t, env, &Visual{}, map[string]any{"id": "cpu-load", "title": " CPU load ", "html": "<canvas></canvas>"})
	if r.IsError || !strings.HasPrefix(text(r), `Showed the visual "CPU load" (id cpu-load)`) || r.Summary != "Showed CPU load" {
		t.Fatalf("shown: %q / %q", text(r), r.Summary)
	}
	if m := r.Structured.(map[string]any); m["shown"] != true || m["id"] != "cpu-load" || m["title"] != "CPU load" {
		t.Fatalf("structured: %v", m)
	}
}

func TestVisualWithoutUI(t *testing.T) {
	env := newEnv(t)
	for _, h := range []Host{nil, &fakeHost{}, &fakeHost{ia: &fakeIA{}}, &fakeHost{ia: &htmlIA{}}} {
		env.SetHost(h)
		r := call(t, env, &Visual{}, map[string]any{"id": "a", "title": "A", "html": "<p>a</p>"})
		if r.IsError || !strings.HasPrefix(text(r), "Nothing was shown") || r.Structured.(map[string]any)["shown"] != false {
			t.Fatalf("no UI: %q", text(r))
		}
	}
}
