package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinDocumentsSkill(t *testing.T) {
	c := Discover(Options{Builtin: true})
	s, ok := c.Lookup("documents")
	if !ok {
		t.Fatal("the documents skill is not listed")
	}
	if s.Source != SourceBuiltin || s.FromServer() || s.Description == "" {
		t.Errorf("%+v", s)
	}
	body, err := s.LoadBody()
	if err != nil || !strings.Contains(body, "DocumentEdit") || strings.Contains(body, "name: documents") {
		t.Errorf("body %q, %v", body[:min(len(body), 80)], err)
	}
	if _, ok := Discover(Options{}).Lookup("documents"); ok {
		t.Error("only asked-for discovery adds it")
	}
}

// Sessions that touch no document pay nothing for the skill, and the model can still load it by name.
func TestBuiltinSkillIsListedOnlyOnceADocumentIsTouched(t *testing.T) {
	root := t.TempDir()
	m := manager(Options{Builtin: true, Cwd: root})
	list, _ := m.Listing(200000, true)
	if strings.Contains(list, "documents") {
		t.Fatalf("listed before any document: %q", list)
	}
	if s, ok := m.Catalog(false).Lookup("documents"); !ok || !m.ModelInvocable(m.Catalog(false), s) {
		t.Error("it must stay loadable by name")
	}
	m.NoteFile(filepath.Join(root, "notes.txt"))
	if list, _ = m.Listing(200000, false); strings.Contains(list, "documents") {
		t.Errorf("listed after a text file: %q", list)
	}
	m.NoteFile(filepath.Join(root, "sub", "deck.pptx"))
	if list, _ = m.Listing(200000, false); !strings.Contains(list, "documents") {
		t.Errorf("not listed after a deck: %q", list)
	}
}

func TestBuiltinSkillYieldsToAUserSkill(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "documents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte("---\nname: documents\ndescription: mine\n---\nMine.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, ok := Discover(Options{HarnessHome: home, Builtin: true}).Lookup("documents")
	if !ok || s.Source != SourcePersonal {
		t.Errorf("%+v", s)
	}
}
