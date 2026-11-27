package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/xlsft/harness/packages/client/completion"
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/selfupdate"
)

func TestCompletionScripts(t *testing.T) {
	for _, sh := range completion.Shells {
		var b bytes.Buffer
		if err := completion.Write(&b, sh, commandTree()); err != nil {
			t.Fatal(err)
		}
		script := b.String()
		for _, want := range []string{"doctor", "completion", "update", "serve", "output-format", "permission-mode", "no-mcp"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s: %s missing", sh, want)
			}
		}
		// Every flag of a headless run completes.
		fs, _ := newPrintFlags()
		fs.VisitAll(func(f *pflag.Flag) {
			if !strings.Contains(script, f.Name) {
				t.Errorf("%s: --%s missing", sh, f.Name)
			}
		})
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		file := filepath.Join(t.TempDir(), "c")
		if err := os.WriteFile(file, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "-n", file).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, out)
		}
	}
	if runCompletion([]string{"powershell"}) != 2 {
		t.Error("an unknown shell is a usage error")
	}
}

// stdout runs f and returns what it printed.
func stdout(t *testing.T, f func() int) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := f()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), code
}

func TestUpdateStopsAtThePolicyMaximum(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/xlsft/harness/releases/latest" {
			http.Redirect(w, r, "/xlsft/harness/releases/tag/v0.5.0", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()
	oldWeb, oldVersion, oldBounds := releasesWeb, config.Version, policyBounds
	defer func() { releasesWeb, config.Version, policyBounds = oldWeb, oldVersion, oldBounds }()
	releasesWeb, config.Version = gh.URL, "0.3.0"

	policyBounds = func(context.Context) selfupdate.Bounds { return selfupdate.Bounds{} }
	out, code := stdout(t, func() int { return runUpdate(context.Background(), []string{"--check"}) })
	if code != 0 || !strings.Contains(out, "v0.5.0 is available") {
		t.Fatalf("no policy: %d %q", code, out)
	}
	policyBounds = func(context.Context) selfupdate.Bounds { return selfupdate.Bounds{Max: "0.4.2"} }
	out, code = stdout(t, func() int { return runUpdate(context.Background(), []string{"--check"}) })
	if code != 0 || !strings.Contains(out, "v0.4.2 is available") || !strings.Contains(out, "allows up to 0.4.2") {
		t.Fatalf("capped: %d %q", code, out)
	}
	config.Version = "0.4.2"
	out, code = stdout(t, func() int { return runUpdate(context.Background(), []string{"--check"}) })
	if code != 0 || !strings.Contains(out, "allows versions up to 0.4.2") {
		t.Fatalf("at the maximum: %d %q", code, out)
	}
}
