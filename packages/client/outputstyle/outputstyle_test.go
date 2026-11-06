package outputstyle_test

import (
	"os"
	"path/filepath"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/outputstyle"
	"github.com/xlsft/harness/packages/client/prompts"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bundle() *prompts.Bundle {
	return &prompts.Bundle{OutputStyles: map[string]prompts.OutputStyle{
		"default":     {ID: "default", Name: "Default", KeepCodingInstructions: true},
		"learning":    {ID: "learning", Name: "Learning", Description: "hands-on", Text: "LEARN", KeepCodingInstructions: true},
		"explanatory": {ID: "explanatory", Name: "Explanatory", Description: "insights", Text: "EXPLAIN", KeepCodingInstructions: true},
		"concise":     {ID: "concise", Name: "Concise", Text: "SHORT"},
	}}
}

func TestParseFrontmatter(t *testing.T) {
	st := outputstyle.Parse("---\nname: Terse Reviewer\ndescription: \"Short reviews\"\nkeep-coding-instructions: true\n---\n\nBe terse.\n", "terse")
	if st.ID != "Terse Reviewer" || st.Name != "Terse Reviewer" || st.Description != "Short reviews" || !st.KeepCodingInstructions || st.Text != "Be terse." {
		t.Fatalf("parsed %+v", st)
	}
	// Defaults: the name is the file stem and coding instructions are dropped (as in Claude Code).
	st = outputstyle.Parse("---\ndescription: d\n---\nBody", "stem")
	if st.Name != "stem" || st.ID != "stem" || st.KeepCodingInstructions || st.Text != "Body" {
		t.Fatalf("defaults %+v", st)
	}
	st = outputstyle.Parse("No frontmatter at all.", "plain")
	if st.Name != "plain" || st.Text != "No frontmatter at all." || st.Description != "" {
		t.Fatalf("no frontmatter %+v", st)
	}
	if outputstyle.Parse("---\nkeep-coding-instructions: yes\n---\nx", "a").KeepCodingInstructions != true {
		t.Fatal("yes must parse as true")
	}
}

func TestDiscoverPrecedence(t *testing.T) {
	home, hh, root := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(root, "sub")
	write(t, filepath.Join(hh, "output-styles", "explanatory.md"), "---\ndescription: user explanatory\n---\nUSER EXPLAIN")
	write(t, filepath.Join(home, ".claude", "output-styles", "gone.md"), "Not read.")
	write(t, filepath.Join(cwd, ".claude", "output-styles", "gone2.md"), "Not read.")
	write(t, filepath.Join(hh, "output-styles", "pirate.md"), "---\ndescription: harness pirate\n---\nArr.")
	write(t, filepath.Join(hh, "output-styles", "notes.txt"), "ignored")
	write(t, filepath.Join(root, ".harness", "output-styles", "team.md"), "---\nname: Team\n---\nROOT TEAM")
	write(t, filepath.Join(cwd, ".harness", "output-styles", "team.md"), "---\nname: team\nkeep-coding-instructions: true\n---\nCWD TEAM")
	write(t, filepath.Join(cwd, ".harness", "output-styles", "Learning.md"), "PROJECT LEARNING")

	c := outputstyle.Discover(outputstyle.Options{Bundle: bundle(), HarnessHome: hh, ProjectDirs: []string{root, cwd}})
	got := map[string]outputstyle.Style{}
	var order []string
	for _, st := range c.List() {
		got[st.ID] = st
		order = append(order, st.ID)
	}
	if order[0] != "default" {
		t.Fatalf("default first: %v", order)
	}
	if st := got["explanatory"]; st.Source != outputstyle.SourceUser || st.Text != "USER EXPLAIN" || st.Description != "user explanatory" {
		t.Fatalf("user style must replace the built-in: %+v", st)
	}
	if st, _ := c.Lookup("pirate"); st.Text != "Arr." || st.Source != outputstyle.SourceUser {
		t.Fatalf("~/.harness: %+v", st)
	}
	if st, _ := c.Lookup("TEAM"); st.Text != "CWD TEAM" || st.Source != outputstyle.SourceProject || !st.KeepCodingInstructions {
		t.Fatalf("cwd project style wins over the git root: %+v", st)
	}
	if st, _ := c.Lookup("learning"); st.Text != "PROJECT LEARNING" || st.Source != outputstyle.SourceProject {
		t.Fatalf("project style named like a built-in replaces it: %+v", st)
	}
	if st, ok := c.Lookup("Concise"); !ok || st.Source != outputstyle.SourceBuiltin || st.KeepCodingInstructions {
		t.Fatalf("built-in lookup by name: %+v %v", st, ok)
	}
	if _, ok := c.Lookup("nope"); ok {
		t.Fatal("unknown style found")
	}
	if _, ok := got["gone"]; ok {
		t.Fatal("~/.claude/output-styles was read")
	}
	if _, ok := got["gone2"]; ok {
		t.Fatal(".claude/output-styles was read")
	}
	if _, ok := got["notes"]; ok {
		t.Fatal("non-markdown file loaded")
	}
	// Built-ins in Claude Code order (replaced in place), then custom styles.
	want := []string{"default", "concise", "explanatory", "Learning", "pirate", "team"}
	if len(order) != len(want) {
		t.Fatalf("styles %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("styles %v, want %v", order, want)
		}
	}
}

func TestDiscoverEmptyBundle(t *testing.T) {
	c := outputstyle.Discover(outputstyle.Options{Bundle: &prompts.Bundle{}})
	l := c.List()
	if len(l) != 1 || l[0].ID != "default" {
		t.Fatalf("an empty bundle still offers the default: %+v", l)
	}
	if st, ok := c.Lookup("default"); !ok || !outputstyle.IsDefault(st.ID) {
		t.Fatal("default lookup")
	}
	if !outputstyle.IsDefault("") || !outputstyle.IsDefault(" Default ") || outputstyle.IsDefault("learning") {
		t.Fatal("IsDefault")
	}
}

type docs struct {
	synced bool
	list   []*syncv1.Document
}

func (d docs) Synced() bool { return d.synced }
func (d docs) List(kind syncv1.DocumentKind) []*syncv1.Document {
	var out []*syncv1.Document
	for _, x := range d.list {
		if x.GetKind() == kind {
			out = append(out, x)
		}
	}
	return out
}

func TestDiscoverSyncDocuments(t *testing.T) {
	hh := t.TempDir()
	write(t, filepath.Join(hh, "output-styles", "local.md"), "LOCAL")
	write(t, filepath.Join(hh, "output-styles", "Terse.md"), "LOCAL TERSE")
	d := docs{synced: true, list: []*syncv1.Document{
		{Id: "doc_1", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_OUTPUT_STYLE, Key: "Terse", Content: "---\ndescription: synced\n---\nTERSE"},
		{Id: "doc_2", Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_OUTPUT_STYLE, Key: "Org", Content: "ORG"},
		{Id: "doc_3", Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_SKILL, Key: "skill", Content: "SKILL"},
	}}
	c := outputstyle.Discover(outputstyle.Options{Docs: d, HarnessHome: hh})
	if st, ok := c.Lookup("terse"); !ok || st.Text != "TERSE" || st.Source != outputstyle.SourceUser || st.Origin != "sync:doc_1" {
		t.Fatalf("synced style wins over the device's: %+v %v", st, ok)
	}
	if _, ok := c.Lookup("local"); !ok {
		t.Fatal("~/.harness/output-styles counts after a sync too")
	}
	for _, n := range []string{"Org", "skill"} {
		if _, ok := c.Lookup(n); ok {
			t.Fatalf("%s must not be loaded", n)
		}
	}
	// An unsynced cache says nothing yet.
	d.synced = false
	c = outputstyle.Discover(outputstyle.Options{Docs: d, HarnessHome: hh})
	if st, _ := c.Lookup("terse"); st.Text != "LOCAL TERSE" {
		t.Fatalf("unsynced cache must not be used: %+v", st)
	}
}
