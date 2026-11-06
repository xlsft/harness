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
