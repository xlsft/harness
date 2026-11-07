package completion

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func sample() *Command {
	root := pflag.NewFlagSet("prog", pflag.ContinueOnError)
	root.BoolP("print", "p", false, "run one turn")
	root.String("output-format", "text", "text | json")
	Choices(root, "output-format", "text", "json", "stream-json")
	root.StringArray("add-dir", nil, "another directory: it's quoted")
	Dirs(root, "add-dir")
	root.String("model", "", "model id: any")
	hidden := root.Bool("secret", false, "hidden")
	_ = hidden
	_ = root.MarkHidden("secret")
	serve := pflag.NewFlagSet("serve", pflag.ContinueOnError)
	serve.String("socket", "", "unix socket")
	serve.Bool("verbose", false, "debug logging")
	return &Command{
		Name: "prog", Flags: root,
		Subcommands: []*Command{
			{Name: "serve", Short: "Serve it", Flags: serve},
			{Name: "completion", Short: "Print a completion script", Args: Shells},
			{Name: "plugin", Short: "Plugins", Subcommands: []*Command{{Name: "install", Short: "Install one"}, {Name: "list", Short: "List them"}}},
		},
	}
}

func script(t *testing.T, shell string) string {
	t.Helper()
	var b bytes.Buffer
	if err := Write(&b, shell, sample()); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestUnknownShell(t *testing.T) {
	if err := Write(&bytes.Buffer{}, "powershell", sample()); err == nil {
		t.Fatal("powershell: want an error")
	}
}

func TestHiddenFlagsAreLeftOut(t *testing.T) {
	for _, sh := range Shells {
		if strings.Contains(script(t, sh), "secret") {
			t.Errorf("%s: a hidden flag is offered", sh)
		}
	}
}

func TestSyntax(t *testing.T) {
	for _, sh := range Shells {
		bin, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("%s isn't installed: syntax not checked", sh)
			continue
		}
		f := filepath.Join(t.TempDir(), "c."+sh)
		if err := os.WriteFile(f, []byte(script(t, sh)), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "-n", f).CombinedOutput(); err != nil {
			t.Errorf("%s -n: %v\n%s", sh, err, out)
		}
	}
}

// bashComplete runs the bash script for a command line whose last word is being completed.
func bashComplete(t *testing.T, line string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	f := filepath.Join(t.TempDir(), "c.bash")
	if err := os.WriteFile(f, []byte(script(t, "bash")), 0o644); err != nil {
		t.Fatal(err)
	}
	words := strings.Split(line, " ")
	var quoted []string
	for _, w := range words {
		quoted = append(quoted, shq(w))
	}
	prog := "source " + shq(f) + "\nCOMP_WORDS=(" + strings.Join(quoted, " ") + ")\nCOMP_CWORD=" + strconv.Itoa(len(words)-1) +
		"\ncompopt() { :; }\n_prog\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
	cmd := exec.Command(bash, "--norc", "--noprofile", "-c", prog)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

func TestBashCompletes(t *testing.T) {
	cases := []struct {
		line string
		want []string
		not  []string
	}{
		{"prog ", []string{"serve", "completion", "plugin", "--print", "-p", "--model"}, []string{"install", "--socket"}},
		{"prog se", []string{"serve"}, []string{"plugin"}},
		{"prog serve --", []string{"--socket", "--verbose"}, []string{"--print"}},
		{"prog plugin ", []string{"install", "list"}, []string{"serve"}},
		{"prog completion ", []string{"bash", "zsh", "fish"}, nil},
		{"prog --output-format ", []string{"text", "json", "stream-json"}, []string{"serve"}},
		{"prog --output-format j", []string{"json"}, []string{"text"}},
		// The value of a flag isn't a subcommand: "serve" here is a model id.
		{"prog --model serve --", []string{"--print"}, []string{"--socket"}},
	}
	for _, c := range cases {
		got := bashComplete(t, c.line)
		for _, w := range c.want {
			if !contains(got, w) {
				t.Errorf("%q: %s missing from %v", c.line, w, got)
			}
		}
		for _, w := range c.not {
			if contains(got, w) {
				t.Errorf("%q: %s offered: %v", c.line, w, got)
			}
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestNames(t *testing.T) {
	got := strings.Join(Names(sample()), ",")
	if got != "prog,prog completion,prog plugin,prog plugin install,prog plugin list,prog serve" {
		t.Fatal(got)
	}
}
