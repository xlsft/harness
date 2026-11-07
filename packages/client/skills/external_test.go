package skills_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/skills"
)

func writeSkill(t *testing.T, dir, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.SkillFile), []byte("---\ndescription: "+description+"\n---\nDo it."), 0o644); err != nil {
		t.Fatal(err)
	}
}

func names(c *skills.Catalog) string {
	var out []string
	for _, s := range c.All {
		out = append(out, s.Name)
	}
	return strings.Join(out, ",")
}

func TestPolicyKeepsASkillListedAndFromRunning(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, filepath.Join(home, "skills", "mine"), "Mine")
	writeSkill(t, filepath.Join(home, "skills", "bad"), "Bad")
	c := skills.Discover(skills.Options{HarnessHome: home, Allowed: func(name string) bool { return name != "bad" }})
	if names(c) != "bad,mine" && names(c) != "mine,bad" {
		t.Fatalf("listed: %s", names(c))
	}
	if c.Enabled("bad") || !c.BlockedByPolicy("bad") {
		t.Fatal("the policy didn't hold")
	}
	if !c.Enabled("mine") || c.BlockedByPolicy("mine") {
		t.Fatal("the policy held the wrong skill")
	}
	// The user's switch can't turn it back on: it has no say.
	if on := skills.Discover(skills.Options{HarnessHome: home, Disabled: map[string]bool{"bad": false}, Allowed: func(name string) bool { return name != "bad" }}); on.Enabled("bad") {
		t.Fatal("a preference beat the policy")
	}
}

func TestAStateFileThatCantBeReadHoldsThePersonalFolder(t *testing.T) {
	home := t.TempDir()
	writeSkill(t, filepath.Join(home, "skills", "stored"), "Stored")
	if err := os.WriteFile(filepath.Join(home, "skills-external.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := names(skills.Discover(skills.Options{HarnessHome: home})); got != "" {
		t.Fatalf("loaded %q without knowing what was approved", got)
	}
}
