package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/tools"
)

const savedScript = `export const meta = { name: "deploy", description: "Ship it", phases: ["one"] }
return 1
`

func TestSavedAndSave(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	dirs := []string{home, project}
	m := &Manager{cfg: Config{SearchDirs: func() []string { return dirs }}, runs: map[string]*run{
		"r1":    {rec: record{Info: tools.WorkflowInfo{ID: "r1"}, Script: savedScript}},
		"empty": {rec: record{Info: tools.WorkflowInfo{ID: "empty"}}},
	}}
	if got := m.Saved(); len(got) != 0 || m.HasSaved("deploy") {
		t.Fatalf("%v", got)
	}

	p, err := m.Save("r1", "deploy", false)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(home, "deploy.js") {
		t.Fatalf("saved at %s", p)
	}
	if got, _ := os.ReadFile(p); string(got) != savedScript {
		t.Fatalf("content %q", got)
	}
	if !m.HasSaved("deploy") {
		t.Fatal("not found")
	}
	// It doesn't replace what is there.
	if _, err := m.Save("r1", "deploy", false); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("%v", err)
	}
	// The project's goes to the project's folder, and wins.
	pp, err := m.Save("r1", "deploy", true)
	if err != nil || pp != filepath.Join(project, "deploy.js") {
		t.Fatalf("%s %v", pp, err)
	}
	saved := m.Saved()
	if len(saved) != 1 || saved[0].Path != pp || saved[0].Description != "Ship it" {
		t.Fatalf("%+v", saved)
	}
	for _, bad := range []struct{ id, name string }{{"r1", "../evil"}, {"r1", "a/b"}, {"nope", "x"}, {"empty", "x"}} {
		if _, err := m.Save(bad.id, bad.name, false); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
	// A folder that isn't trusted has no project directory.
	dirs = []string{home}
	if _, err := m.Save("r1", "other", true); err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Fatalf("%v", err)
	}
	// A script that doesn't parse isn't listed.
	if err := os.WriteFile(filepath.Join(home, "broken.js"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, w := range m.Saved() {
		if w.Name == "broken" {
			t.Fatal("broken script listed")
		}
	}
	if m.HasSaved("../deploy") {
		t.Fatal("a path is no name")
	}
}
