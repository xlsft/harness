package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectInstructions(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents root"), 0o644)
	os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("agents sub"), 0o644)
	files := ProjectInstructions(sub)
	if len(files) != 2 || files[0].Content != "agents root" || files[1].Content != "agents sub" {
		t.Fatalf("AGENTS.md fallback: %+v", files)
	}
	os.WriteFile(filepath.Join(root, "a", "CLAUDE.md"), []byte("claude <!-- hidden --> rules"), 0o644)
	files = ProjectInstructions(sub)
	if len(files) != 1 || files[0].Content != "claude  rules" {
		t.Fatalf("CLAUDE.md wins over AGENTS.md: %+v", files)
	}
	out := FormatInstructions(files, "Current branch: main\n")
	if !strings.Contains(out, "claude  rules") || !strings.Contains(out, "gitStatus:\nCurrent branch: main") {
		t.Fatalf("format: %q", out)
	}
	vars := Env{Cwd: "/x", IsGit: true, ModelID: "m"}.Vars()
	if vars["cwd"] != "/x" || vars["is_git"] != "true" || vars["model_id"] != "m" {
		t.Fatalf("vars %v", vars)
	}
}
