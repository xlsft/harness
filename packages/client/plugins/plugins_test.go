package plugins_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/internal/gittest"
	"github.com/xlsft/harness/packages/client/plugins"
	"github.com/xlsft/harness/packages/client/skills"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		gittest.Write(t, dir, rel, text)
	}
}

// fullPlugin is a plugin with one of everything, in the standard layout.
func fullPlugin(name, version string) map[string]string {
	return map[string]string{
		".claude-plugin/plugin.json": `{"name":"` + name + `","version":"` + version + `","description":"A test plugin"}`,
		"skills/fmt/SKILL.md":        "---\ndescription: Formats\nallowed-tools: Bash(prettier:*)\n---\nFormat with ${CLAUDE_PLUGIN_ROOT}/x and keep ${CLAUDE_PLUGIN_DATA}.\n",
		"commands/check.md":          "---\ndescription: Checks\n---\nCheck it\n",
		"commands/sub/deep.md":       "Deep\n",
		"agents/reviewer.md":         "---\nname: reviewer\ndescription: Reviews\n---\nReview at ${CLAUDE_PLUGIN_ROOT}\n",
		"output-styles/terse.md":     "---\nname: terse\n---\nBe terse.\n",
		"hooks/hooks.json":           `{"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"\"${CLAUDE_PLUGIN_ROOT}\"/format.sh"}]}]}}`,
		".mcp.json":                  `{"mcpServers":{"style":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/server.js"]}}}`,
	}
}

func newStore(t *testing.T) *plugins.Manager {
	t.Helper()
	gittest.Isolate(t)
	return &plugins.Manager{Root: filepath.Join(t.TempDir(), "plugins"), PolicyOf: func() plugins.Policy { return plugins.Policy{} }}
}

var bg = context.Background()

// marketRepo makes a git repository holding a marketplace with one plugin in a folder, and returns its URL.
func marketRepo(t *testing.T, version string) (dir, url string) {
	t.Helper()
	dir = gittest.Init(t)
	files := fullPlugin("fmt-tools", version)
	for rel, text := range files {
		gittest.Write(t, dir, "plugins/fmt-tools/"+rel, text)
	}
	write(t, dir, map[string]string{".claude-plugin/marketplace.json": `{"name":"acme","owner":{"name":"Acme"},"description":"Acme's","plugins":[
		{"name":"fmt-tools","source":"./plugins/fmt-tools","description":"Formats"},
		{"name":"gone","source":"./plugins/gone"}]}`})
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-q", "-m", "marketplace")
	return dir, "file://" + dir
}

func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-q", "-m", msg)
	return gittest.Run(t, dir, "rev-parse", "HEAD")
}

func TestLoadStandardLayoutNamespacesEverything(t *testing.T) {
	root := t.TempDir()
	write(t, root, fullPlugin("fmt-tools", "1.0.0"))
	p, err := plugins.Load(root, "fmt-tools", "acme", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Contents
	if !slices.Equal(c.Skills, []string{"fmt-tools:fmt"}) {
		t.Errorf("skills: %v", c.Skills)
	}
	if !slices.Equal(c.Commands, []string{"fmt-tools:check", "fmt-tools:sub:deep"}) {
		t.Errorf("commands: %v", c.Commands)
	}
	if !slices.Equal(c.Agents, []string{"fmt-tools:reviewer"}) || !slices.Equal(c.OutputStyles, []string{"fmt-tools:terse"}) {
		t.Errorf("agents, styles: %v %v", c.Agents, c.OutputStyles)
	}
	if len(c.Hooks) != 1 || c.Hooks[0].Event != "PostToolUse" || !strings.Contains(c.Hooks[0].Command, "format.sh") {
		t.Errorf("hooks: %+v", c.Hooks)
	}
	if len(c.MCPServers) != 1 || c.MCPServers[0].Name != "plugin:fmt-tools:style" || c.MCPServers[0].Transport != "stdio" {
		t.Errorf("mcp: %+v", c.MCPServers)
	}
	if !slices.Equal(c.AllowedTools, []string{"Bash(prettier:*)"}) {
		t.Errorf("allowed tools: %v", c.AllowedTools)
	}
	if len(p.Problems) != 0 {
		t.Errorf("problems: %v", p.Problems)
	}
}

func TestOurManifestLocationWins(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json":  `{"name":"theirs"}`,
		".harness-plugin/plugin.json": `{"name":"ours"}`,
		"commands/a.md":               "A\n",
	})
	p, err := plugins.Load(root, "", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "ours" || !slices.Equal(p.Contents.Commands, []string{"ours:a"}) {
		t.Fatalf("%s %v", p.Name, p.Contents.Commands)
	}
}

func TestManifestWithoutFileUsesFolderName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bare")
	write(t, root, map[string]string{"skills/one/SKILL.md": "One\n"})
	p, err := plugins.Load(root, "bare", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Contents.Skills, []string{"bare:one"}) {
		t.Fatal(p.Contents.Skills)
	}
}

func TestSingleSkillAtTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "solo")
	write(t, root, map[string]string{"SKILL.md": "---\nname: solo-skill\n---\nBody\n"})
	p, err := plugins.Load(root, "solo", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Contents.Skills, []string{"solo:solo-skill"}) {
		t.Fatal(p.Contents.Skills)
	}
}

func TestComponentFieldsReplaceOrAddLikeTheFormat(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		".claude-plugin/plugin.json": `{"name":"p",
			"skills":"./extra-skills",
			"commands":{"about":{"content":"About it","description":"Says what it is"},"status":{"source":"./cmds/status.md"}},
			"agents":["./my/one.md"],
			"outputStyles":"./styles/",
			"hooks":[{"Stop":[{"hooks":[{"type":"command","command":"inline"}]}]},"./more.json"],
			"mcpServers":{"a":{"url":"https://example.test/mcp","type":"http"}},
			"lspServers":{"go":{"command":"gopls"}},"userConfig":{"k":{}}}`,
		"skills/default/SKILL.md": "Default\n",
		"extra-skills/x/SKILL.md": "Extra\n",
		"commands/ignored.md":     "Ignored\n",
		"cmds/status.md":          "Status\n",
		"agents/ignored.md":       "Ignored\n",
		"my/one.md":               "One\n",
		"styles/s.md":             "S\n",
		"output-styles/ignore.md": "Ignored\n",
		"hooks/hooks.json":        `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"default"}]}]}}`,
		"more.json":               `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"file"}]}]}}`,
		".mcp.json":               `{"b":{"command":"b"}}`,
	})
	p, err := plugins.Load(root, "p", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Contents
	if !slices.Equal(c.Skills, []string{"p:default", "p:x"}) {
		t.Errorf("skills add to the default: %v", c.Skills)
	}
	if !slices.Equal(c.Commands, []string{"p:about", "p:status"}) {
		t.Errorf("commands replace it: %v", c.Commands)
	}
	if !slices.Equal(c.Agents, []string{"p:one"}) || !slices.Equal(c.OutputStyles, []string{"p:s"}) {
		t.Errorf("agents, styles replace it: %v %v", c.Agents, c.OutputStyles)
	}
	if len(c.Hooks) != 3 {
		t.Errorf("hooks merge: %+v", c.Hooks)
	}
	if len(c.MCPServers) != 2 {
		t.Errorf("servers merge: %+v", c.MCPServers)
	}
	for _, want := range []string{"language servers (lspServers)", "user configuration (userConfig)"} {
		if !slices.Contains(c.Skipped, want) {
			t.Errorf("skipped %q not reported: %v", want, c.Skipped)
		}
	}
	found := skills.ScanPlugin(p.SkillSource())
	for _, s := range found {
		if s.Name == "p:about" {
			body, err := s.LoadBody()
			if err != nil || body != "About it" || s.Description != "Says what it is" {
				t.Errorf("inline command: %q %q %v", body, s.Description, err)
			}
		}
	}
}

func TestPathsLeavingThePluginAreProblems(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, map[string]string{"secret.md": "secret"})
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, root, map[string]string{".claude-plugin/plugin.json": `{"name":"p","agents":["../x.md","no-prefix.md","./link/secret.md"],"commands":"./missing"}`})
	p, err := plugins.Load(root, "p", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Contents.Agents) != 0 || len(p.Commands) != 0 {
		t.Fatalf("loaded: %v %v", p.Contents.Agents, p.Commands)
	}
	if len(p.Problems) != 4 {
		t.Fatalf("problems: %v", p.Problems)
	}
}

func TestHooksFileNeedsItsWrapper(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"hooks/hooks.json": `{"Stop":[]}`, "commands/a.md": "A"})
	p, err := plugins.Load(root, "p", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.HookBlocks) != 0 || len(p.Problems) != 1 {
		t.Fatalf("%v %v", p.HookBlocks, p.Problems)
	}
}

func TestBadPluginNameRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{".claude-plugin/plugin.json": `{"name":"../evil"}`})
	if _, err := plugins.Load(root, "x", "local", "", nil); err == nil {
		t.Fatal("a name with a path in it loaded")
	}
}

func TestStrictFalseEntryConflictsWithAManifest(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{".claude-plugin/plugin.json": `{"name":"p"}`, "commands/a.md": "A"})
	strict := false
	_, err := plugins.Load(root, "p", "m", "", &plugins.Overlay{Strict: &strict, Manifest: plugins.Manifest{Commands: json.RawMessage(`"./commands"`)}})
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatal(err)
	}
}

func TestMarketplaceFromGitAndTheGates(t *testing.T) {
	m := newStore(t)
	dir, url := marketRepo(t, "1.0.0")
	src, err := plugins.ParseSource(url)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := m.AddMarketplace(bg, src)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "acme" || rec.Commit != gittest.Run(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("%+v", rec)
	}
	markets, err := m.Marketplaces()
	if err != nil || len(markets) != 1 || len(markets[0].Plugins) != 2 {
		t.Fatalf("%+v %v", markets, err)
	}
	if _, err := m.Install(bg, "gone@acme"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing folder: %v", err)
	}
	got, err := m.Install(bg, "fmt-tools@acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.0.0" || got.Hash == "" {
		t.Fatalf("%+v", got)
	}

	trusted := plugins.Place{Cwd: t.TempDir(), Trusted: true}
	untrusted := plugins.Place{Cwd: t.TempDir(), Trusted: false}
	id := "fmt-tools@acme"
	status := func(place plugins.Place) plugins.Status {
		t.Helper()
		in, err := m.Find(id, place)
		if err != nil {
			t.Fatal(err)
		}
		return in.Status
	}
	if s := status(trusted); s != plugins.StatusNeedsApproval {
		t.Fatalf("after install: %s", s)
	}
	if len(m.Active(trusted)) != 0 {
		t.Fatal("an unapproved plugin is active")
	}
	if err := m.Approve(id, "sha256:not-it"); err == nil {
		t.Fatal("approved content nobody saw")
	}
	if err := m.Approve(id, got.Hash); err != nil {
		t.Fatal(err)
	}
	if s := status(trusted); s != plugins.StatusActive {
		t.Fatalf("after approve: %s", s)
	}
	if s := status(untrusted); s != plugins.StatusUntrustedFolder {
		t.Fatalf("untrusted folder: %s", s)
	}
	if len(m.Active(untrusted)) != 0 {
		t.Fatal("active in an untrusted folder")
	}
	if len(m.Active(trusted)) != 1 {
		t.Fatal("not active")
	}
	if err := m.SetEnabled(id, false); err != nil {
		t.Fatal(err)
	}
	if s := status(trusted); s != plugins.StatusDisabled {
		t.Fatalf("disabled: %s", s)
	}
	if err := m.SetEnabled(id, true); err != nil {
		t.Fatal(err)
	}

	// The files change under it: it stops, and can't be approved back.
	in, _ := m.Find(id, trusted)
	gittest.Write(t, in.Root, "hooks/hooks.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"evil"}]}]}}`)
	if s := status(trusted); s != plugins.StatusChangedOnDisk {
		t.Fatalf("tampered: %s", s)
	}
	if len(m.Active(trusted)) != 0 {
		t.Fatal("tampered and active")
	}
	if err := m.Approve(id, ""); err == nil {
		t.Fatal("approved files that do not match the install")
	}
}

func TestUpdateNeedsApprovingAgainAndVersionsHold(t *testing.T) {
	m := newStore(t)
	dir, url := marketRepo(t, "1.0.0")
	src, _ := plugins.ParseSource(url)
	if _, err := m.AddMarketplace(bg, src); err != nil {
		t.Fatal(err)
	}
	id := "fmt-tools@acme"
	rec, err := m.Install(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Approve(id, rec.Hash); err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(m.Root, rec.Dir)

	// New files under the same version: it stays where it is, as the format's version field says.
	gittest.Write(t, dir, "plugins/fmt-tools/commands/new.md", "New\n")
	commit(t, dir, "same version")
	res, err := m.Update(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("moved within a version: %+v", res.Record)
	}

	// Pinned: it doesn't look.
	if err := m.SetPinned(id, true); err != nil {
		t.Fatal(err)
	}
	gittest.Write(t, dir, "plugins/fmt-tools/.claude-plugin/plugin.json", `{"name":"fmt-tools","version":"2.0.0"}`)
	commit(t, dir, "two")
	res, err = m.Update(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || !strings.Contains(res.Note, "pinned") {
		t.Fatalf("%+v", res)
	}
	if err := m.SetPinned(id, false); err != nil {
		t.Fatal(err)
	}

	res, err = m.Update(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Record.Version != "2.0.0" {
		t.Fatalf("%+v", res)
	}
	place := plugins.Place{Cwd: t.TempDir(), Trusted: true}
	in, _ := m.Find(id, place)
	if in.Status != plugins.StatusNeedsApproval {
		t.Fatalf("new content runs unapproved: %s", in.Status)
	}
	if !slices.Contains(in.Contents.Commands, "fmt-tools:new") {
		t.Fatalf("%v", in.Contents.Commands)
	}
	// The previous version stays for sessions still running it.
	if !isDir(oldRoot) {
		t.Fatal("the previous version is gone")
	}
	// Approval and the switch carried over the way they should: enabled yes, approved no.
	if !in.Enabled || in.Approved {
		t.Fatalf("%+v", in)
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func TestInstallFromFolderIsACopyNotALink(t *testing.T) {
	m := newStore(t)
	src := filepath.Join(t.TempDir(), "my-plugin")
	write(t, src, fullPlugin("my-plugin", "0.1.0"))
	rec, err := m.Install(bg, src)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "my-plugin@local" {
		t.Fatal(rec.ID)
	}
	if err := m.Approve(rec.ID, rec.Hash); err != nil {
		t.Fatal(err)
	}
	gittest.Write(t, src, "commands/later.md", "Later\n")
	in, _ := m.Find(rec.ID, plugins.Place{Cwd: t.TempDir(), Trusted: true})
	if in.Status != plugins.StatusActive || slices.Contains(in.Contents.Commands, "my-plugin:later") {
		t.Fatalf("%s %v", in.Status, in.Contents.Commands)
	}
	res, err := m.Update(bg, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("a folder's update found nothing")
	}
}

func TestSymlinkOutOfThePluginRefusesTheCopy(t *testing.T) {
	m := newStore(t)
	secret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secret, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "p")
	write(t, src, fullPlugin("p", "1"))
	if err := os.Symlink(secret, filepath.Join(src, "skills", "fmt", "key")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(bg, src); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatal(err)
	}
}

func TestPluginSourcesFromGit(t *testing.T) {
	m := newStore(t)
	repo := gittest.Init(t)
	write(t, repo, map[string]string{"tools/sub/.claude-plugin/plugin.json": `{"name":"sub"}`, "tools/sub/commands/a.md": "A\n"})
	first := commit(t, repo, "first")
	gittest.Write(t, repo, "tools/sub/commands/b.md", "B\n")
	commit(t, repo, "second")
	gittest.Run(t, repo, "config", "uploadpack.allowFilter", "true")

	market := t.TempDir()
	write(t, market, map[string]string{".claude-plugin/marketplace.json": `{"name":"mk","owner":{"name":"x"},"plugins":[
		{"name":"sub","source":{"source":"git-subdir","url":"file://` + repo + `","path":"tools/sub","sha":"` + first + `"}},
		{"name":"wrong","source":{"source":"url","url":"file://` + repo + `","sha":"0000000000000000000000000000000000000000"}},
		{"name":"npm-one","source":{"source":"npm","package":"x"}}]}`})
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
		t.Fatal(err)
	}
	rec, err := m.Install(bg, "sub@mk")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Commit != first {
		t.Fatalf("pinned %s, got %s", first, rec.Commit)
	}
	in, _ := m.Find(rec.ID, plugins.Place{})
	if !slices.Equal(in.Contents.Commands, []string{"sub:a"}) {
		t.Fatalf("the pin was not honored: %v", in.Contents.Commands)
	}
	if _, err := m.Install(bg, "wrong@mk"); err == nil {
		t.Fatal("a sha that is not there")
	}
	if _, err := m.Install(bg, "npm-one@mk"); err == nil || !strings.Contains(err.Error(), "does not fetch") {
		t.Fatal(err)
	}
}

func TestDirectInstallFromRepository(t *testing.T) {
	m := newStore(t)
	repo := gittest.Init(t)
	write(t, repo, fullPlugin("repo-plugin", "3.1.0"))
	head := commit(t, repo, "plugin")
	rec, err := m.Install(bg, "file://"+repo)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "repo-plugin@local" || rec.Commit != head || rec.Version != "3.1.0" {
		t.Fatalf("%+v", rec)
	}
}

func TestMarketplaceFromAURL(t *testing.T) {
	m := newStore(t)
	repo := gittest.Init(t)
	write(t, repo, fullPlugin("remote", "1.0.0"))
	commit(t, repo, "plugin")
	body := `{"name":"hosted","owner":{"name":"x"},"plugins":[
		{"name":"remote","source":{"source":"url","url":"file://` + repo + `"}},
		{"name":"relative","source":"./plugins/relative"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	src, err := plugins.ParseSource(srv.URL + "/marketplace.json")
	if err != nil || src.Source != plugins.SourceURL {
		t.Fatalf("%+v %v", src, err)
	}
	if _, err := m.AddMarketplace(bg, src); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(bg, "remote@hosted"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(bg, "relative@hosted"); err == nil || !strings.Contains(err.Error(), "no plugin folders") {
		t.Fatal(err)
	}
}

func TestParseSourceForms(t *testing.T) {
	for text, want := range map[string]plugins.Source{
		"acme/tools":                     {Source: plugins.SourceGitHub, Repo: "acme/tools"},
		"acme/tools#v2":                  {Source: plugins.SourceGitHub, Repo: "acme/tools", Ref: "v2"},
		"git@git.example.test:a/b.git":   {Source: plugins.SourceGit, URL: "git@git.example.test:a/b.git"},
		"https://example.test/a/b.git":   {Source: plugins.SourceGit, URL: "https://example.test/a/b.git"},
		"https://example.test/m/mk.json": {Source: plugins.SourceURL, URL: "https://example.test/m/mk.json"},
		"/abs/folder":                    {Source: plugins.SourceDirectory, Path: "/abs/folder"},
	} {
		got, err := plugins.ParseSource(text)
		if err != nil {
			t.Errorf("%s: %v", text, err)
			continue
		}
		if got.Source != want.Source || got.Repo != want.Repo || got.URL != want.URL || got.Path != want.Path || got.Ref != want.Ref {
			t.Errorf("%s: %+v", text, got)
		}
	}
	for _, bad := range []string{"", "-upload-pack=evil", "ext::sh -c evil", "ftp://example.test/x"} {
		if _, err := plugins.ParseSource(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestRemoteTransportsThatRunProgramsAreRefused(t *testing.T) {
	m := newStore(t)
	for _, url := range []string{"ext::sh -c touch${IFS}/tmp/pwned", "--upload-pack=evil", "http://example.test/plain.git"} {
		market := t.TempDir()
		data, _ := json.Marshal(map[string]any{"name": "mk", "owner": map[string]string{"name": "x"}, "plugins": []any{map[string]any{"name": "p", "source": map[string]string{"source": "url", "url": url}}}})
		write(t, market, map[string]string{".claude-plugin/marketplace.json": string(data)})
		if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Install(bg, "p@mk"); err == nil {
			t.Errorf("%s was fetched", url)
		}
		if err := m.RemoveMarketplace("mk"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReservedAndBadMarketplaceNames(t *testing.T) {
	m := newStore(t)
	for _, name := range []string{"local", "claude-plugins-official", "../up"} {
		market := t.TempDir()
		write(t, market, map[string]string{".claude-plugin/marketplace.json": `{"name":"` + name + `","owner":{"name":"x"},"plugins":[]}`})
		if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err == nil {
			t.Errorf("%s was registered", name)
		}
	}
}

func TestSameNameFromAnotherPlaceIsRefused(t *testing.T) {
	m := newStore(t)
	a := filepath.Join(t.TempDir(), "same")
	write(t, a, map[string]string{"commands/a.md": "A"})
	if _, err := m.Install(bg, a); err != nil {
		t.Fatal(err)
	}
	market := t.TempDir()
	write(t, market, map[string]string{".claude-plugin/marketplace.json": `{"name":"mk","owner":{"name":"x"},"plugins":[{"name":"same","source":"./same"}]}`, "same/commands/b.md": "B"})
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(bg, "same@mk"); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatal(err)
	}
}

func TestRemoveDeletesFilesAndData(t *testing.T) {
	m := newStore(t)
	src := filepath.Join(t.TempDir(), "p")
	write(t, src, fullPlugin("p", "1"))
	rec, err := m.Install(bg, src)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := m.Find(rec.ID, plugins.Place{})
	if err := m.Remove(rec.ID, false); err != nil {
		t.Fatal(err)
	}
	if isDir(in.Root) {
		t.Fatal("files stayed")
	}
	if _, err := m.Find(rec.ID, plugins.Place{}); err == nil {
		t.Fatal("still listed")
	}
	if err := m.Remove(rec.ID, false); err == nil {
		t.Fatal("removed twice")
	}
}

func TestEntryIsTheManifestWithoutAPluginJSON(t *testing.T) {
	m := newStore(t)
	market := t.TempDir()
	write(t, market, map[string]string{
		".claude-plugin/marketplace.json": `{"name":"mk","owner":{"name":"x"},"metadata":{"pluginRoot":"./plugins"},"plugins":[
			{"name":"inline","source":"inline","description":"From the entry","version":"2.0.0","commands":"./c","strict":false,"defaultEnabled":false}]}`,
		"plugins/inline/c/a.md": "A\n",
	})
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceDirectory, Path: market}); err != nil {
		t.Fatal(err)
	}
	rec, err := m.Install(bg, "inline@mk")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Version != "2.0.0" || rec.Enabled {
		t.Fatalf("%+v", rec)
	}
	// The state file brings the entry back: nulls in it must not read as paths.
	in, err := m.Find(rec.ID, plugins.Place{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in.Contents.Commands, []string{"inline:a"}) || in.Description != "From the entry" || len(in.Problems) != 0 {
		t.Fatalf("%+v", in)
	}
}

// ---- policy ----

func policyDoc(content string) *syncv1.Document {
	return &syncv1.Document{Scope: commonv1.Scope_SCOPE_ORG, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Key: "default", Content: content}
}

func TestPolicyFromDocsMerges(t *testing.T) {
	p := plugins.PolicyFromDocs([]*syncv1.Document{
		policyDoc(`{"strictKnownMarketplaces":[{"source":"github","repo":"acme/*"}],"blockedMarketplaces":[{"source":"github","repo":"acme/bad"}],"enabledPlugins":{"a@m":true,"b@m":false}}`),
		policyDoc(`{"allowedMarketplaces":[{"source":"hostPattern","hostPattern":"^github\\.com$"}],"extraKnownMarketplaces":{"m":{"source":{"source":"github","repo":"acme/m"}}},"enabledPlugins":{"a@m":false},"allowedMcpServers":["ok"]}`),
		{Scope: commonv1.Scope_SCOPE_USER, Kind: syncv1.DocumentKind_DOCUMENT_KIND_POLICY, Content: `{"strictKnownMarketplaces":[]}`},
	})
	if len(p.Allowlists) != 2 {
		t.Fatalf("%+v", p.Allowlists)
	}
	gh := func(repo string) plugins.Source { return plugins.Source{Source: plugins.SourceGitHub, Repo: repo} }
	if why := p.SourceRefusal(gh("acme/good")); why != "" {
		t.Error(why)
	}
	if p.SourceRefusal(gh("acme/bad")) == "" || p.SourceRefusal(gh("other/x")) == "" {
		t.Error("a blocked or unlisted source passed")
	}
	if !p.PluginBlocked("a@m") || !p.PluginBlocked("b@m") {
		t.Error("false in any document blocks")
	}
	if p.Distributed["m"].Repo != "acme/m" || !p.MCPAllowed("plugin:x:ok", "ok") || p.MCPAllowed("plugin:x:no", "no") {
		t.Errorf("%+v", p)
	}
}

func TestPolicyRulesMatchSources(t *testing.T) {
	policy := func(rules ...plugins.Rule) plugins.Policy { return plugins.Policy{Allowlists: [][]plugins.Rule{rules}} }
	cases := []struct {
		name string
		rule plugins.Rule
		src  plugins.Source
		ok   bool
	}{
		{"repo", plugins.Rule{Source: "github", Repo: "a/b"}, plugins.Source{Source: "github", Repo: "a/b"}, true},
		{"ref must match", plugins.Rule{Source: "github", Repo: "a/b"}, plugins.Source{Source: "github", Repo: "a/b", Ref: "main"}, false},
		{"owner", plugins.Rule{Source: "github", Repo: "a/*"}, plugins.Source{Source: "github", Repo: "a/z"}, true},
		{"owner is whole", plugins.Rule{Source: "github", Repo: "a/*"}, plugins.Source{Source: "github", Repo: "ab/z"}, false},
		{"spellings of one repo", plugins.Rule{Source: "git", URL: "git@git.example.test:a/b.git"}, plugins.Source{Source: "git", URL: "https://git.example.test/a/b"}, true},
		{"url", plugins.Rule{Source: "url", URL: "https://x.test/m.json"}, plugins.Source{Source: "url", URL: "https://x.test/m.json"}, true},
		{"host", plugins.Rule{Source: "hostPattern", HostPattern: `^git\.corp\.test$`}, plugins.Source{Source: "git", URL: "https://git.corp.test/a.git"}, true},
		{"other host", plugins.Rule{Source: "hostPattern", HostPattern: `^git\.corp\.test$`}, plugins.Source{Source: "git", URL: "https://evil.test/a.git"}, false},
		{"github is a host", plugins.Rule{Source: "hostPattern", HostPattern: `^github\.com$`}, plugins.Source{Source: "github", Repo: "a/b"}, true},
		{"path", plugins.Rule{Source: "pathPattern", PathPattern: `^/opt/`}, plugins.Source{Source: "directory", Path: "/opt/x"}, true},
		{"bad pattern allows nothing", plugins.Rule{Source: "hostPattern", HostPattern: `(`}, plugins.Source{Source: "git", URL: "https://x.test/a"}, false},
		{"folder", plugins.Rule{Source: "directory", Path: "/opt/m"}, plugins.Source{Source: "directory", Path: "/opt/m/"}, true},
	}
	for _, c := range cases {
		if got := policy(c.rule).SourceRefusal(c.src) == ""; got != c.ok {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if (plugins.Policy{Allowlists: [][]plugins.Rule{{}}}).SourceRefusal(plugins.Source{Source: "github", Repo: "a/b"}) == "" {
		t.Error("an empty allowlist allowed a source")
	}
}

func TestPolicyStopsWhatIsAlreadyInstalled(t *testing.T) {
	m := newStore(t)
	_, url := marketRepo(t, "1.0.0")
	src, _ := plugins.ParseSource(url)
	if _, err := m.AddMarketplace(bg, src); err != nil {
		t.Fatal(err)
	}
	id := "fmt-tools@acme"
	rec, err := m.Install(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Approve(id, rec.Hash); err != nil {
		t.Fatal(err)
	}
	place := plugins.Place{Cwd: t.TempDir(), Trusted: true}
	if len(m.Active(place)) != 1 {
		t.Fatal("not active")
	}
	m.PolicyOf = func() plugins.Policy {
		return plugins.Policy{Allowlists: [][]plugins.Rule{{{Source: "github", Repo: "corp/*"}}}}
	}
	in, _ := m.Find(id, place)
	if in.Status != plugins.StatusBlockedByPolicy || len(m.Active(place)) != 0 {
		t.Fatalf("%s", in.Status)
	}
	if _, err := m.AddMarketplace(bg, plugins.Source{Source: plugins.SourceGit, URL: url + "2"}); err == nil {
		t.Fatal("a marketplace outside the list was added")
	}
	if _, err := m.Install(bg, id); err == nil {
		t.Fatal("installed from a marketplace outside the list")
	}
	if _, err := m.Install(bg, t.TempDir()); err == nil {
		t.Fatal("installed from a folder outside the list")
	}
	m.PolicyOf = func() plugins.Policy { return plugins.Policy{Plugins: map[string]bool{id: false}} }
	in, _ = m.Find(id, place)
	if in.Status != plugins.StatusBlockedByPolicy {
		t.Fatalf("%s", in.Status)
	}
	if err := m.SetEnabled(id, true); err == nil {
		t.Fatal("turned on what the policy blocks")
	}
}

func TestDistributionFetchesAndStillNeedsApproval(t *testing.T) {
	m := newStore(t)
	_, url := marketRepo(t, "1.0.0")
	id := "fmt-tools@acme"
	pol := plugins.Policy{Distributed: map[string]plugins.Source{"acme": {Source: plugins.SourceGit, URL: url}}, Plugins: map[string]bool{id: true}}
	m.PolicyOf = func() plugins.Policy { return pol }
	place := plugins.Place{Cwd: t.TempDir(), Trusted: true}
	in, err := m.Find(id, place)
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != plugins.StatusNotInstalled || !in.Managed {
		t.Fatalf("before: %+v", in)
	}
	if errs := m.SyncDistributed(bg); len(errs) != 0 {
		t.Fatal(errs)
	}
	in, err = m.Find(id, place)
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != plugins.StatusNeedsApproval || !in.Managed {
		t.Fatalf("after: %+v", in)
	}
	if len(m.Active(place)) != 0 {
		t.Fatal("the organization's word made it run")
	}
	if err := m.Approve(id, in.Hash); err != nil {
		t.Fatal(err)
	}
	if len(m.Active(place)) != 1 {
		t.Fatal("approved and not active")
	}
	if err := m.SetEnabled(id, false); err == nil {
		t.Fatal("turned off what the organization keeps on")
	}
	if err := m.Remove(id, false); err == nil {
		t.Fatal("removed what the organization distributes")
	}
	if err := m.RemoveMarketplace("acme"); err == nil {
		t.Fatal("removed the organization's marketplace")
	}
	// Running it again changes nothing.
	if errs := m.SyncDistributed(bg); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestPolicyIsRememberedForOfflineStarts(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(plugins.ResetPolicy)
	plugins.SetPolicy(root, plugins.Policy{Plugins: map[string]bool{"a@b": false}})
	plugins.ResetPolicy()
	if !plugins.CurrentPolicy(root).PluginBlocked("a@b") {
		t.Fatal("the last policy was forgotten")
	}
}

func TestProjectSettingsSwitchButCannotApprove(t *testing.T) {
	m := newStore(t)
	src := filepath.Join(t.TempDir(), "p")
	write(t, src, fullPlugin("p", "1"))
	rec, err := m.Install(bg, src)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	write(t, cwd, map[string]string{".claude/settings.json": `{"enabledPlugins":{"p@local":false,"other@mk":true},"extraKnownMarketplaces":{"mk":{"source":{"source":"github","repo":"a/b"}}}}`})
	trusted := plugins.Place{Cwd: cwd, Trusted: true}
	list, err := m.List(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Status != plugins.StatusDisabled || list[1].ID != "other@mk" || list[1].Status != plugins.StatusNotInstalled || list[1].SuggestedBy == "" {
		t.Fatalf("%+v", list)
	}
	if m.Suggestions(trusted)["mk"].Repo != "a/b" {
		t.Fatal("no suggestion")
	}
	// An untrusted folder gets no say at all.
	in, _ := m.Find(rec.ID, plugins.Place{Cwd: cwd})
	if !in.Enabled || m.Suggestions(plugins.Place{Cwd: cwd}) != nil {
		t.Fatalf("%+v", in)
	}
	// Asking for a plugin doesn't approve it.
	write(t, cwd, map[string]string{".claude/settings.json": `{"enabledPlugins":{"p@local":true}}`})
	in, _ = m.Find(rec.ID, trusted)
	if in.Status != plugins.StatusNeedsApproval {
		t.Fatal(in.Status)
	}
	// The user's switch off, with the project on: on here.
	if err := m.Approve(rec.ID, rec.Hash); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(rec.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(m.Active(trusted)) != 1 {
		t.Fatal("the project's switch doesn't count")
	}
	if len(m.Active(plugins.Place{Cwd: t.TempDir(), Trusted: true})) != 0 {
		t.Fatal("the user's switch doesn't count elsewhere")
	}
}

// ---- commands ----

func TestCommandsWalkThrough(t *testing.T) {
	m := newStore(t)
	_, url := marketRepo(t, "1.0.0")
	cwd := t.TempDir()
	c := plugins.Caller{Place: plugins.Place{Cwd: cwd, Trusted: true}}
	run := func(words ...string) string {
		t.Helper()
		out, err := plugins.RunCommand(bg, m, words, c)
		if err != nil {
			t.Fatalf("%v: %v", words, err)
		}
		return out
	}
	run("marketplace", "add", url)
	if out := run("marketplace", "list"); !strings.Contains(out, "fmt-tools@acme") {
		t.Fatal(out)
	}
	out := run("install", "fmt-tools@acme")
	for _, want := range []string{"Nothing of it runs yet", "format.sh", "plugin:fmt-tools:style", "/plugin approve fmt-tools@acme"} {
		if !strings.Contains(out, want) {
			t.Errorf("install doesn't say %q:\n%s", want, out)
		}
	}
	if out := run("list"); !strings.Contains(out, "needs approval") {
		t.Fatal(out)
	}
	if out := run("approve", "fmt-tools"); !strings.Contains(out, "Approved") {
		t.Fatal(out)
	}
	if out := run(); !strings.Contains(out, "runs here") {
		t.Fatal(out)
	}
	run("pin", "fmt-tools")
	if out := run("update"); !strings.Contains(out, "pinned") {
		t.Fatal(out)
	}
	run("unpin", "fmt-tools")
	run("disable", "fmt-tools")
	run("enable", "fmt-tools")
	run("revoke", "fmt-tools")
	run("remove", "fmt-tools@acme")
	run("marketplace", "remove", "acme")
	if out := run("marketplace"); !strings.Contains(out, "No marketplaces") {
		t.Fatal(out)
	}
	if _, err := plugins.RunCommand(bg, m, []string{"nonsense"}, c); err == nil {
		t.Fatal("a nonsense command")
	}
}

func TestRemoteCallersOnlyLook(t *testing.T) {
	m := newStore(t)
	src := filepath.Join(t.TempDir(), "p")
	write(t, src, fullPlugin("p", "1"))
	if _, err := m.Install(bg, src); err != nil {
		t.Fatal(err)
	}
	c := plugins.Caller{Remote: true}
	for _, words := range [][]string{{"approve", "p"}, {"install", src}, {"disable", "p"}, {"remove", "p"}, {"marketplace", "add", "a/b"}, {"update"}, {"new", "x"}} {
		if _, err := plugins.RunCommand(bg, m, words, c); err == nil || !strings.Contains(err.Error(), "device") {
			t.Errorf("%v: %v", words, err)
		}
	}
	for _, words := range [][]string{{}, {"list"}, {"info", "p"}, {"marketplace", "list"}} {
		if _, err := plugins.RunCommand(bg, m, words, c); err != nil {
			t.Errorf("%v: %v", words, err)
		}
	}
	in, _ := m.Find("p@local", plugins.Place{})
	if in.Approved {
		t.Fatal("approved from another machine")
	}
}

func TestApproveCanBeRefusedHere(t *testing.T) {
	m := newStore(t)
	src := filepath.Join(t.TempDir(), "p")
	write(t, src, fullPlugin("p", "1"))
	if _, err := m.Install(bg, src); err != nil {
		t.Fatal(err)
	}
	_, err := plugins.RunCommand(bg, m, []string{"approve", "p"}, plugins.Caller{NoApprove: "needs a terminal"})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatal(err)
	}
}

func TestScaffoldMakesAPluginThatValidatesAndInstalls(t *testing.T) {
	m := newStore(t)
	cwd := t.TempDir()
	c := plugins.Caller{Place: plugins.Place{Cwd: cwd, Trusted: true}}
	if _, err := plugins.RunCommand(bg, m, []string{"new", "my-tool"}, c); err != nil {
		t.Fatal(err)
	}
	out, err := plugins.RunCommand(bg, m, []string{"validate", "my-tool"}, c)
	if err != nil || !strings.Contains(out, "valid") {
		t.Fatalf("%s %v", out, err)
	}
	rec, err := m.Install(bg, filepath.Join(cwd, "my-tool"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Version != "0.1.0" {
		t.Fatalf("%+v", rec)
	}
	if err := plugins.Scaffold(filepath.Join(cwd, "my-tool"), "my-tool"); err == nil {
		t.Fatal("wrote over a plugin")
	}
	if err := plugins.Scaffold(filepath.Join(cwd, "x"), "../x"); err == nil {
		t.Fatal("a bad name")
	}
}

func TestValidateMarketplaceSaysWhatIsWrong(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{".claude-plugin/marketplace.json": `{"name":"mk","owner":{"name":"x"},"plugins":[
		{"name":"a","source":"./ok"},{"name":"a","source":"./ok"},{"name":"b","source":"../up"},{"name":"c","source":"./broken"}]}`,
		"ok/commands/x.md": "X", "broken/.claude-plugin/plugin.json": "{"})
	_, err := plugins.Validate(dir)
	if err == nil {
		t.Fatal("no problems found")
	}
	for _, want := range []string{"duplicate plugin name a", "b:", "c:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no %q in:\n%v", want, err)
		}
	}
}
