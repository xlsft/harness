//go:build unix

package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xlsft/harness/packages/client/agent"
	"github.com/xlsft/harness/packages/client/bootstrap"
	fg "github.com/xlsft/harness/packages/client/internal/fakegateway"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/llm"
	"github.com/xlsft/harness/packages/client/permissions"
)

// A rewind undoes what a turn changed through Bash: a heredoc, sed -i, a new file and a deleted one, in a git
// repository and in a folder outside git.
func TestRewindUndoesBashEdits(t *testing.T) {
	const script = "cat > notes.txt <<'EOF'\nrewritten\nEOF\n" +
		"sed -i.bak 's/colour/color/' style.txt && rm style.txt.bak\n" +
		"mkdir -p out && echo generated > out/new.txt\n" +
		"rm old.txt"
	for _, inGit := range []bool{true, false} {
		name := "folder"
		if inGit {
			name = "git"
		}
		t.Run(name, func(t *testing.T) {
			e := setup(t, func(n int, r *llm.Request) fg.Reply {
				last := r.Messages[len(r.Messages)-1]
				if last.Content[len(last.Content)-1].Type == llm.BlockToolResult {
					return fg.Reply{Events: fg.Message("m", fg.Text("done"))}
				}
				return fg.Reply{Events: fg.Message("m", fg.ToolUse("b1", "Bash", map[string]any{"description": "edit", "command": script}))}
			})
			gittest.Isolate(t)
			files := map[string]string{"notes.txt": "original\n", "style.txt": "colour\n", "old.txt": "keep\n"}
			for p, c := range files {
				gittest.Write(t, e.proj, p, c)
			}
			if inGit {
				gittest.Run(t, e.proj, "init", "-q")
				gittest.Run(t, e.proj, "add", "-A")
				gittest.Run(t, e.proj, "commit", "-q", "-m", "files")
			}
			s := e.open(t, func(o *bootstrap.Options) { o.PermissionMode = permissions.ModeBypassPermissions })
			ctx := context.Background()
			s.Agent.RunTurn(ctx, prompt("edit through the shell"))
			if data, _ := os.ReadFile(filepath.Join(e.proj, "notes.txt")); string(data) != "rewritten\n" {
				t.Fatalf("the shell did not run: %q", data)
			}
			cps := s.Agent.Checkpoints()
			if len(cps) != 1 || !cps[0].HasCodeChanges {
				t.Fatalf("checkpoints: %+v", cps)
			}
			res, err := s.Agent.Rewind(ctx, cps[0].ID, agent.RewindCode, "")
			if err != nil || res.FilesRestored != 4 || res.FilesSkipped != 0 {
				t.Fatalf("rewind: %+v %v", res, err)
			}
			for p, want := range files {
				if data, _ := os.ReadFile(filepath.Join(e.proj, p)); string(data) != want {
					t.Errorf("%s = %q, want %q", p, data, want)
				}
			}
			if _, err := os.Stat(filepath.Join(e.proj, "out")); !os.IsNotExist(err) {
				t.Error("the generated folder is still there")
			}
		})
	}
}
