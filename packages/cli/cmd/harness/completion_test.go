package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/completion"
)

func TestCompletionScripts(t *testing.T) {
	for _, sh := range completion.Shells {
		var b bytes.Buffer
		if err := completion.Write(&b, sh, commandTree()); err != nil {
			t.Fatal(err)
		}
		fs, _ := newMainFlags()
		fs.VisitAll(func(f *pflag.Flag) {
			if !strings.Contains(b.String(), f.Name) {
				t.Errorf("%s: --%s missing", sh, f.Name)
			}
		})
		if bin, err := exec.LookPath(sh); err == nil {
			file := filepath.Join(t.TempDir(), "c")
			if err := os.WriteFile(file, b.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, "-n", file).CombinedOutput(); err != nil {
				t.Errorf("%s -n: %v\n%s", sh, err, out)
			}
		}
	}
}
