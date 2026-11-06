package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var flagRe = regexp.MustCompile(`(?:^|[\s(])--([a-z][a-z-]+)`)

// The templates call the binary with flags: a renamed flag must fail here, not in somebody's CI.
func checkFlags(t *testing.T, name, text string, known func(string) bool) {
	t.Helper()
	for _, m := range flagRe.FindAllStringSubmatch(text, -1) {
		if !known(m[1]) {
			t.Errorf("%s uses --%s, which harness-client does not have", name, m[1])
		}
	}
}

func headlessFlagNames(t *testing.T) func(string) bool {
	t.Helper()
	// cmdPrint builds its flags itself; its help text lists them.
	source := string(repoFile(t, "packages/client/cmd/harness-client/main.go"))
	return func(name string) bool { return strings.Contains(source, `"`+name+`"`) }
}

func ciFlagKnown(name string) bool { return newCIFlags().fs.Lookup(name) != nil }

func TestGitHubActionIsValidAndUsesRealFlags(t *testing.T) {
	var action struct {
		Inputs map[string]struct {
			Required bool   `yaml:"required"`
			Default  string `yaml:"default"`
		} `yaml:"inputs"`
		Runs struct {
			Using string `yaml:"using"`
			Steps []struct {
				ID  string            `yaml:"id"`
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(repoFile(t, "ci/github/action.yml"), &action); err != nil {
		t.Fatal(err)
	}
	if action.Runs.Using != "composite" {
		t.Fatalf("using %q", action.Runs.Using)
	}
	for _, want := range []string{"mode", "server-url", "device-token", "max-budget-usd", "allowed-tools", "json-schema"} {
		if _, ok := action.Inputs[want]; !ok {
			t.Errorf("input %s is missing", want)
		}
	}
	headless := headlessFlagNames(t)
	found := false
	for _, s := range action.Runs.Steps {
		if s.ID != "harness" {
			continue
		}
		found = true
		for _, line := range strings.Split(s.Run, "\n") {
			switch {
			case strings.Contains(line, "ci review"), strings.Contains(line, "ci mention"), strings.Contains(line, "--allowed-tools"), strings.Contains(line, "args+=("), strings.Contains(line, "args=("):
				checkFlags(t, "action.yml", line, func(n string) bool { return ciFlagKnown(n) || headless(n) })
			case strings.Contains(line, " -p "):
				checkFlags(t, "action.yml", line, headless)
			}
		}
		// A value from a comment or a title must never land inside the script's text.
		if strings.Contains(s.Run, "${{") {
			t.Errorf("the script expands an expression, which is how shell injection happens: %s", s.Run)
		}
	}
	if !found {
		t.Fatal("no run step")
	}
}

func TestGitHubExamplesAreValid(t *testing.T) {
	for _, name := range []string{"harness-review.yml", "harness-mention.yml"} {
		var wf map[string]any
		if err := yaml.Unmarshal(repoFile(t, filepath.Join("ci/github/examples", name)), &wf); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if wf["jobs"] == nil {
			t.Fatalf("%s has no jobs", name)
		}
	}
	// The review example must never run with secrets on a fork's code.
	if strings.Contains(string(repoFile(t, "ci/github/examples/harness-review.yml")), "\n  pull_request_target") {
		t.Fatal("the review example uses pull_request_target")
	}
}

func TestGitLabTemplateIsValidAndUsesRealFlags(t *testing.T) {
	var tpl map[string]any
	data := repoFile(t, "ci/gitlab/harness.gitlab-ci.yml")
	if err := yaml.Unmarshal(data, &tpl); err != nil {
		t.Fatal(err)
	}
	for _, job := range []string{"harness-review", "harness-mention"} {
		j, ok := tpl[job].(map[string]any)
		if !ok {
			t.Fatalf("job %s is missing", job)
		}
		if j["extends"] != ".harness" || j["rules"] == nil {
			t.Fatalf("job %s: %+v", job, j)
		}
		for _, line := range j["script"].([]any) {
			checkFlags(t, job, line.(string), ciFlagKnown)
		}
	}
}
