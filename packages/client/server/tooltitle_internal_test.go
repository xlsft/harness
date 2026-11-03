package server

import (
	"strings"
	"testing"
)

func TestCleanToolTitle(t *testing.T) {
	for in, want := range map[string]string{
		"unit tests":                      "unit tests",
		"Unit tests.":                     "unit tests",
		"Run unit tests":                  "unit tests",
		"Running the page script":         "the page script",
		"\"Ran dependency install.\"":     "dependency install",
		"Label: lint on the UI package":   "lint on the UI package",
		"CSV export":                      "CSV export",
		"Git status of the repo":          "Git status of the repo",
		"GitHub PR list":                  "GitHub PR list",
		"Docker image build":              "Docker image build",
		"Runner logs":                     "runner logs",
		"run":                             "run",
		"## **Page script on localhost**": "page script on localhost",
		"":                                "",
	} {
		if got := cleanToolTitle(in); got != want {
			t.Errorf("cleanToolTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanToolTitle(strings.Repeat("word ", 30)); len([]rune(got)) > maxToolTitleRunes+1 {
		t.Errorf("not truncated: %q", got)
	}
}

func TestToolTitleInput(t *testing.T) {
	got := toolTitleInput("  go test ./...  ", "Run the tests")
	if got != "<command>\ngo test ./...\n</command>\n<description>Run the tests</description>" {
		t.Fatalf("input: %q", got)
	}
	if got := toolTitleInput("ls", " "); strings.Contains(got, "description") {
		t.Fatalf("empty description rendered: %q", got)
	}
	if got := toolTitleInput(strings.Repeat("é", 2000), ""); len(got) > toolTitleCommandChars+40 {
		t.Fatalf("command not clipped: %d", len(got))
	}
}
