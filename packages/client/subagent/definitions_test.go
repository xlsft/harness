package subagent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xlsft/harness/packages/client/subagent"
)

func writeAgent(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionsLeaveClaudeFoldersAlone(t *testing.T) {
	home, hh, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, filepath.Join(home, ".claude", "agents"), "user-claude", "no")
	writeAgent(t, filepath.Join(cwd, ".claude", "agents"), "project-claude", "no")
	writeAgent(t, filepath.Join(hh, "agents"), "user", "user body")
	writeAgent(t, filepath.Join(cwd, ".harness", "agents"), "project", "project body")

	got := map[string]string{}
	for _, ty := range subagent.LoadDefinitions(subagent.DefinitionDirs(hh, cwd, true)) {
		got[ty.Name] = ty.Instructions
	}
	if len(got) != 2 || got["user"] != "user body" || got["project"] != "project body" {
		t.Fatalf("%v", got)
	}
}

func TestSyncedDefinitionsYieldToDeviceFiles(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "both", "from the device")
	docs := []subagent.Document{{Key: "both", Content: "from the server"}, {Key: "only", Content: "synced body"}, {Key: subagent.TypeGeneral, Content: "not a built-in"}}
	got := map[string]string{}
	for _, ty := range subagent.LoadDefinitionsOver(docs, []string{dir}) {
		got[ty.Name] = ty.Instructions
	}
	if len(got) != 2 || got["both"] != "from the device" || got["only"] != "synced body" {
		t.Fatalf("%v", got)
	}
}
