package plugins_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/plugins"
)

const offLimits = "contents that must never leave the server"

// partialRepo makes a repository with a wanted folder, a folder that must not be fetched and a license at its root.
func partialRepo(t *testing.T, filters bool) (dir, head string) {
	t.Helper()
	dir = gittest.Init(t)
	write(t, dir, map[string]string{
		"LICENSE":                           "The root license.\n",
		"README.md":                         "Root file, not asked for.\n",
		"skills/wanted/SKILL.md":            "---\nname: wanted\ndescription: Wanted\n---\n\nWanted.\n",
		"skills/wanted/scripts/run.sh":      "echo wanted\n",
		"skills/forbidden/SKILL.md":         "---\nname: forbidden\ndescription: " + offLimits + "\n---\n\n" + offLimits + "\n",
		"skills/forbidden/scripts/x.py":     offLimits + " too\n",
		"skills/wanted-not/SKILL.md":        "---\nname: wanted-not\ndescription: A sibling with a like name\n---\n\n" + offLimits + " as well\n",
		"plugin/.claude-plugin/plugin.json": `{"name":"kit","version":"1.0.0"}`,
		"plugin/commands/go.md":             "Go\n",
		"other/LICENSE":                     "Another license.\n",
	})
	head = commit(t, dir, "fixture")
	if filters {
		gittest.Run(t, dir, "config", "uploadpack.allowFilter", "true")
	}
	return dir, head
}

// fetched runs the partial fetch into a new folder and returns it.
func fetched(t *testing.T, repo, sha, folder string, besides ...string) string {
	t.Helper()
	gittest.Isolate(t)
	dest := filepath.Join(t.TempDir(), "repo")
	got, err := plugins.FetchSubdir(bg, plugins.Source{Source: plugins.SourceSubdir, URL: "file://" + repo, Path: folder, Sha: sha}, dest, besides)
	if err != nil {
		t.Fatal(err)
	}
	if sha != "" && got != sha {
		t.Fatalf("fetched %s, pinned %s", got, sha)
	}
	return dest
}

// objectContents reads every object the clone holds, uncompressed, and asks the server for none.
func objectContents(t *testing.T, dest string) string {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "--batch-all-objects", "--batch")
	cmd.Dir = dest
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSubdirFetchLeavesTheRestOnTheServer(t *testing.T) {
	repo, head := partialRepo(t, true)
	dest := fetched(t, repo, head, "skills/wanted")
	if held := objectContents(t, dest); strings.Contains(held, offLimits) || strings.Contains(held, "Root file") {
		t.Fatal("the object store holds files outside the folder")
	}
	if !strings.Contains(objectContents(t, dest), "echo wanted") {
		t.Fatal("the folder itself was not fetched")
	}
	var names []string
	_ = filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dest, p)
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if strings.Join(names, " ") != "skills/wanted/SKILL.md skills/wanted/scripts/run.sh" {
		t.Fatalf("checked out: %v", names)
	}
}

func TestSubdirFetchBringsADeclaredFileAndNothingNearIt(t *testing.T) {
	repo, head := partialRepo(t, true)
	dest := fetched(t, repo, head, "skills/wanted", "LICENSE")
	if data, err := os.ReadFile(filepath.Join(dest, "LICENSE")); err != nil || string(data) != "The root license.\n" {
		t.Fatalf("%q %v", data, err)
	}
	held := objectContents(t, dest)
	if strings.Contains(held, offLimits) || strings.Contains(held, "Root file") || strings.Contains(held, "Another license") {
		t.Fatal("the object store holds files it wasn't asked for")
	}
}

func TestSubdirFetchIsRefusedWhenTheServerCannotFilter(t *testing.T) {
	gittest.Isolate(t)
	repo, head := partialRepo(t, false)
	dest := filepath.Join(t.TempDir(), "repo")
	_, err := plugins.FetchSubdir(bg, plugins.Source{Source: plugins.SourceSubdir, URL: "file://" + repo, Path: "skills/wanted", Sha: head}, dest, nil)
	if err == nil || !strings.Contains(err.Error(), "can't send part of a repository") {
		t.Fatal(err)
	}
	// Refused before anything came: there is no clone to hold it.
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("something was fetched before the refusal")
	}
}

func TestSubdirFetchPinsAndRefs(t *testing.T) {
	repo, first := partialRepo(t, true)
	gittest.Write(t, repo, "skills/wanted/late.md", "Late\n")
	second := commit(t, repo, "second")
	gittest.Isolate(t)
	if _, err := os.Stat(filepath.Join(fetched(t, repo, first, "skills/wanted"), "skills/wanted/late.md")); err == nil {
		t.Fatal("the pin was not honored")
	}
	dest := filepath.Join(t.TempDir(), "repo")
	got, err := plugins.FetchSubdir(bg, plugins.Source{Source: plugins.SourceSubdir, URL: "file://" + repo, Path: "skills/wanted"}, dest, nil)
	if err != nil || got != second {
		t.Fatalf("%s %v, want %s", got, err, second)
	}
	dest = filepath.Join(t.TempDir(), "repo")
	if _, err := plugins.FetchSubdir(bg, plugins.Source{Source: plugins.SourceSubdir, URL: "file://" + repo, Path: "skills/wanted", Sha: strings.Repeat("0", 40)}, dest, nil); err == nil {
		t.Fatal("a pin that isn't there")
	}
}

func TestSubdirFetchRefusesWhatIsNotItsFolder(t *testing.T) {
	gittest.Isolate(t)
	repo, _ := partialRepo(t, true)
	if err := os.Symlink("forbidden", filepath.Join(repo, "skills", "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(repo, "LICENSE-link")); err != nil {
		t.Fatal(err)
	}
	head := commit(t, repo, "links")
	for name, c := range map[string]struct {
		folder  string
		besides []string
		want    string
	}{
		"a file":             {"README.md", nil, "not a folder"},
		"missing":            {"skills/none", nil, "not a folder"},
		"a link to a folder": {"skills/alias", nil, "not a folder"},
		"a pattern":          {"skills/*", nil, "not a plain path"},
		"a negation":         {"!skills", nil, "not a plain path"},
		"a climb":            {"skills/wanted/../forbidden", nil, "not a plain path"},
		"the root":           {".", nil, "whole repository"},
		"absolute":           {"/skills/wanted", nil, "not a plain path"},
		"into git":           {".git/hooks", nil, "not a plain path"},
		"a pattern besides":  {"skills/wanted", []string{"skills/*"}, "not a plain path"},
		"a link besides":     {"skills/wanted", []string{"LICENSE-link"}, "not a file"},
		"a folder besides":   {"skills/wanted", []string{"skills/forbidden"}, "not a file"},
	} {
		dest := filepath.Join(t.TempDir(), "repo")
		_, err := plugins.FetchSubdir(bg, plugins.Source{Source: plugins.SourceSubdir, URL: "file://" + repo, Path: c.folder, Sha: head}, dest, c.besides)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// licenseMarket adds a marketplace of one entry from the repository, and returns the store.
func licenseMarket(t *testing.T, repo, sha, folder, licenseFile string) *plugins.Manager {
	t.Helper()
	m := newStore(t)
	entry := map[string]any{"name": "kit", "license": "Apache-2.0", "source": map[string]string{"source": "git-subdir", "url": "file://" + repo, "path": folder, "sha": sha}}
	if licenseFile != "" {
		entry["licenseFile"] = licenseFile
	}
	data, _ := json.Marshal(map[string]any{"name": "mk", "owner": map[string]string{"name": "x"}, "plugins": []any{entry}})
	market := t.TempDir()
	write(t, market, map[string]string{".claude-plugin/marketplace.json": string(data)})
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
		t.Fatal(err)
	}
	return m
}

func installedFile(t *testing.T, m *plugins.Manager, rec *plugins.Record, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(m.Root, rec.Dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAnInstallCarriesTheDeclaredLicense(t *testing.T) {
	repo, head := partialRepo(t, true)
	m := licenseMarket(t, repo, head, "plugin", "LICENSE")
	rec, err := m.Install(bg, "kit@mk")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Commit != head {
		t.Fatalf("at %s", rec.Commit)
	}
	if got := installedFile(t, m, rec, "LICENSE"); got != "The root license.\n" {
		t.Fatal(got)
	}
	// It is part of what is approved, and nothing else of the root came along.
	hash, err := plugins.HashTree(filepath.Join(m.Root, rec.Dir))
	if err != nil || hash != rec.Hash {
		t.Fatalf("%s %v, recorded %s", hash, err, rec.Hash)
	}
	if _, err := os.Stat(filepath.Join(m.Root, rec.Dir, "README.md")); err == nil {
		t.Fatal("a root file came along")
	}
}

func TestTheLicenseIsTheOneAtThePin(t *testing.T) {
	repo, first := partialRepo(t, true)
	gittest.Write(t, repo, "LICENSE", "Changed later.\n")
	commit(t, repo, "relicense")
	m := licenseMarket(t, repo, first, "plugin", "LICENSE")
	rec, err := m.Install(bg, "kit@mk")
	if err != nil {
		t.Fatal(err)
	}
	if got := installedFile(t, m, rec, "LICENSE"); got != "The root license.\n" {
		t.Fatalf("not the file at the pin: %q", got)
	}
}

func TestALicenseInsideThePluginIsLeftAlone(t *testing.T) {
	repo, _ := partialRepo(t, true)
	gittest.Write(t, repo, "plugin/LICENSE", "The plugin's own.\n")
	head := commit(t, repo, "own license")
	m := licenseMarket(t, repo, head, "plugin", "plugin/LICENSE")
	rec, err := m.Install(bg, "kit@mk")
	if err != nil {
		t.Fatal(err)
	}
	if got := installedFile(t, m, rec, "LICENSE"); got != "The plugin's own.\n" {
		t.Fatal(got)
	}
}

func TestADeclaredLicenseThatCannotBeCarriedRefusesTheInstall(t *testing.T) {
	repo, _ := partialRepo(t, true)
	gittest.Write(t, repo, "plugin/LICENSE", "The plugin's own.\n")
	if err := os.Symlink("README.md", filepath.Join(repo, "LICENSE-link")); err != nil {
		t.Fatal(err)
	}
	head := commit(t, repo, "own license")
	for file, want := range map[string]string{
		"LICENSE":            "already has a different LICENSE",
		"MISSING-LICENSE":    "isn't named like a license",
		"LICENSE.md":         "not a file",
		"LICENSE-link":       "not a file",
		"plugin/LICENSE.txt": "not in the plugin",
		".mcp.json":          "isn't named like a license",
		"README.md":          "isn't named like a license",
		"../LICENSE":         "not a plain path",
		"skills/*":           "not a plain path",
	} {
		m := licenseMarket(t, repo, head, "plugin", file)
		if _, err := m.Install(bg, "kit@mk"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", file, err)
		}
	}
}

func TestAnInstallFromAServerThatCannotFilterIsRefused(t *testing.T) {
	repo, head := partialRepo(t, false)
	m := licenseMarket(t, repo, head, "plugin", "")
	if _, err := m.Install(bg, "kit@mk"); err == nil || !strings.Contains(err.Error(), "can't send part of a repository") {
		t.Fatal(err)
	}
	st, err := m.List(plugins.Place{})
	if err != nil || len(st) != 0 {
		t.Fatalf("%v %v", st, err)
	}
}

func TestALicenseFromTheMarketplaceFolder(t *testing.T) {
	m := newStore(t)
	market := t.TempDir()
	write(t, market, map[string]string{
		"LICENSE":                    "The catalog's license.\n",
		"plugins/own/commands/go.md": "Go\n",
		".claude-plugin/marketplace.json": `{"name":"mk","owner":{"name":"x"},"plugins":[
			{"name":"own","license":"MIT","licenseFile":"LICENSE","source":"./plugins/own"},
			{"name":"gone","license":"MIT","licenseFile":"LICENSE.txt","source":"./plugins/own"}]}`,
	})
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
		t.Fatal(err)
	}
	rec, err := m.Install(bg, "own@mk")
	if err != nil {
		t.Fatal(err)
	}
	if got := installedFile(t, m, rec, "LICENSE"); got != "The catalog's license.\n" {
		t.Fatal(got)
	}
	if _, err := m.Install(bg, "gone@mk"); err == nil || !strings.Contains(err.Error(), "not there") {
		t.Fatal(err)
	}
	if _, err := plugins.Validate(market); err == nil || !strings.Contains(err.Error(), "LICENSE.txt") {
		t.Fatal(err)
	}
}

func TestLicenseStaysAStringBesideItsFile(t *testing.T) {
	// license is the format's string. The path has its own field, and a field the client doesn't know is passed over.
	m := newStore(t)
	market := t.TempDir()
	write(t, market, map[string]string{
		"plugins/own/commands/go.md":      "Go\n",
		"plugins/own/LICENSE":             "Own.\n",
		".claude-plugin/marketplace.json": `{"name":"mk","owner":{"name":"x"},"plugins":[{"name":"own","license":"MIT","licenseFile":"plugins/own/LICENSE","catalog":{"anything":true},"source":"./plugins/own"}]}`,
	})
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
		t.Fatal(err)
	}
	rec, err := m.Install(bg, "own@mk")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Overlay == nil || rec.Overlay.License != "MIT" || rec.Overlay.LicenseFile != "plugins/own/LICENSE" {
		t.Fatalf("%+v", rec.Overlay)
	}
}
