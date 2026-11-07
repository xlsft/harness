package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/xlsft/harness/packages/client/bootstrap"
	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
	"github.com/xlsft/harness/packages/client/gen/harness/agent/v1/agentv1connect"
	skillsv1 "github.com/xlsft/harness/packages/client/gen/harness/skills/v1"
	"github.com/xlsft/harness/packages/client/rpc"
	"github.com/xlsft/harness/packages/client/server"
)

func (h *harness) extensions() agentv1connect.ExtensionsServiceClient {
	return agentv1connect.NewExtensionsServiceClient(rpc.UnixHTTPClient(h.socket), "http://agent", connect.WithGRPC())
}

func TestExtensionSkillsCreateListRemove(t *testing.T) {
	home := t.TempDir()
	h := startPlugged(t, (&recording{}).respond, true, func(o *bootstrap.Options) { o.SkillHome = home })
	ctx := context.Background()
	api := h.extensions()
	project := t.TempDir()

	for _, bad := range []string{"", "../up", "a b", "-x"} {
		if _, err := api.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Name: bad})); err == nil {
			t.Fatalf("the name %q was taken", bad)
		}
	}
	made, err := api.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Name: "mine", Description: "Does: \"things\"\nand more"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "skills", "mine", "SKILL.md"); made.Msg.GetPath() != want {
		t.Fatalf("path %q, want %q", made.Msg.GetPath(), want)
	}
	if _, err := api.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Name: "mine"})); err == nil {
		t.Fatal("overwrote a skill")
	}
	inProject, err := api.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Name: "here", Cwd: project, Body: "Do it here."}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inProject.Msg.GetPath(), filepath.Join(project, ".harness", "skills")) && !strings.Contains(inProject.Msg.GetPath(), filepath.Join(".harness", "skills", "here")) {
		t.Fatalf("project path %q", inProject.Msg.GetPath())
	}

	list, err := api.ListExtensionSkills(ctx, connect.NewRequest(&agentv1.ListExtensionSkillsRequest{Cwd: project}))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, g := range list.Msg.GetGroups() {
		keys = append(keys, g.GetKey())
	}
	if !slices.Equal(keys, []string{"project", "personal", "builtin"}) {
		t.Fatalf("groups %v", keys)
	}
	row := list.Msg.GetGroups()[1].GetSkills()[0]
	if row.GetCommand().GetName() != "mine" || !row.GetCanRemove() || row.GetUpdatedAt() == "" || row.GetFolder() == "" {
		t.Fatalf("row %+v", row)
	}
	if row.GetCommand().GetDescription() != `Does: "things" and more` {
		t.Fatalf("description %q", row.GetCommand().GetDescription())
	}

	detail, err := api.GetSkillDetail(ctx, connect.NewRequest(&agentv1.GetSkillDetailRequest{Name: "here", Path: list.Msg.GetGroups()[0].GetSkills()[0].GetCommand().GetPath(), Cwd: project}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Msg.GetBody(), "Do it here.") || !slices.Contains(detail.Msg.GetFiles(), "SKILL.md") {
		t.Fatalf("detail %+v", detail.Msg)
	}
	outside := filepath.Join(home, "secret.md")
	writeFile(t, outside, "secret")
	if _, err := api.GetSkillDetail(ctx, connect.NewRequest(&agentv1.GetSkillDetailRequest{Name: "mine", Path: outside})); err == nil {
		t.Fatal("read a file the catalog doesn't know")
	}
	if _, err := api.RemoveSkill(ctx, connect.NewRequest(&agentv1.RemoveSkillRequest{Name: "mine", Path: outside})); err == nil {
		t.Fatal("removed by a path the catalog doesn't know")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}

	if _, err := api.RemoveSkill(ctx, connect.NewRequest(&agentv1.RemoveSkillRequest{Name: "mine", Path: row.GetCommand().GetPath()})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(made.Msg.GetPath())); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
}

func TestCreateSkillNeedsATrustedFolder(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, false)
	project := t.TempDir()
	if _, err := h.extensions().CreateSkill(context.Background(), connect.NewRequest(&agentv1.CreateSkillRequest{Name: "here", Cwd: project})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".harness")); !os.IsNotExist(err) {
		t.Fatalf("wrote in an untrusted folder: %v", err)
	}
}

func TestExtensionPluginsAndConnectorsFoldAPlugin(t *testing.T) {
	marks := t.TempDir()
	h := startPlugged(t, (&recording{}).respond, true)
	ctx := context.Background()
	dir := writePlugin(t, marks)
	installed, err := h.plugins().InstallPlugin(ctx, connect.NewRequest(&agentv1.InstallPluginRequest{Spec: dir}))
	if err != nil {
		t.Fatal(err)
	}
	id := installed.Msg.GetPlugin().GetId()

	list, err := h.extensions().ListExtensionPlugins(ctx, connect.NewRequest(&agentv1.ListExtensionPluginsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetGroups()) != 1 || list.Msg.GetGroups()[0].GetKey() != "local" || list.Msg.GetGroups()[0].GetPlugins()[0].GetPlugin().GetId() != id {
		t.Fatalf("groups %+v", list.Msg.GetGroups())
	}
	conns, err := h.extensions().ListExtensionConnectors(ctx, connect.NewRequest(&agentv1.ListExtensionConnectorsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(conns.Msg.GetConnectors(), func(c *agentv1.ExtensionConnector) bool { return c.GetName() == "srv" })
	if i < 0 {
		t.Fatalf("no plugin server in %+v", conns.Msg.GetConnectors())
	}
	c := conns.Msg.GetConnectors()[i]
	if c.GetStatus() != agentv1.ConnectorStatus_CONNECTOR_STATUS_NEEDS_APPROVAL || c.GetProviders()[0].GetPluginId() != id {
		t.Fatalf("connector %+v", c)
	}
	if !strings.Contains(strings.Join(c.GetBadges(), ","), "Plugin") {
		t.Fatalf("badges %v", c.GetBadges())
	}
}

func TestDiscoverExtensionsThroughTheFakeRegistry(t *testing.T) {
	hits := 0
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"servers":[{"server":{"name":"com.example/one","title":"One","description":"First","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://one.example/mcp"}]},"_meta":{}}],"metadata":{}}`))
	}))
	defer reg.Close()
	t.Setenv("HARNESS_MCP_REGISTRY_URL", reg.URL)
	h := startPlugged(t, (&recording{}).respond, true)
	ctx := context.Background()
	kind := agentv1.ExtensionKind_EXTENSION_KIND_CONNECTOR
	res, err := h.extensions().DiscoverExtensions(ctx, connect.NewRequest(&agentv1.DiscoverExtensionsRequest{Kind: kind}))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, s := range res.Msg.GetSections() {
		keys = append(keys, s.GetKey())
	}
	if !slices.Equal(keys, []string{"catalog", "registry"}) || res.Msg.GetFeatured() == nil {
		t.Fatalf("sections %v, featured %v", keys, res.Msg.GetFeatured())
	}
	if _, err := h.extensions().DiscoverExtensions(ctx, connect.NewRequest(&agentv1.DiscoverExtensionsRequest{Kind: kind})); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("%d requests with a fresh cache", hits)
	}
	plugs, err := h.extensions().DiscoverExtensions(ctx, connect.NewRequest(&agentv1.DiscoverExtensionsRequest{Kind: agentv1.ExtensionKind_EXTENSION_KIND_PLUGIN}))
	if err != nil {
		t.Fatal(err)
	}
	if plugs.Msg.GetFeatured().GetPluginId() == "" {
		t.Fatalf("nothing of the built-in catalog to offer on a first run: %+v", plugs.Msg)
	}
}

func TestSkillChangesAreRefusedFromAnotherMachine(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, true)
	home := os.Getenv("HARNESS_HOME")
	s := server.New(bootstrap.Options{})
	ctx := server.WithRemoteOrigin(h.ctx)
	if _, err := s.CreateSkill(ctx, connect.NewRequest(&agentv1.CreateSkillRequest{Name: "mine"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills")); !os.IsNotExist(err) {
		t.Fatalf("wrote a skill: %v", err)
	}
	path := filepath.Join(home, "skills", "mine", "SKILL.md")
	writeFile(t, path, "---\nname: mine\n---\nDo it.")
	if _, err := s.RemoveSkill(ctx, connect.NewRequest(&agentv1.RemoveSkillRequest{Name: "mine", Path: path})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// fakeSkillSearch answers with one skill, and can change it.
type fakeSkillSearch struct {
	hash string
	path string
}

func (f *fakeSkillSearch) Available(context.Context) (bool, string, error) { return true, "", nil }
func (f *fakeSkillSearch) Search(context.Context, string, int) ([]*skillsv1.ExternalSkill, error) {
	return nil, nil
}
func (f *fakeSkillSearch) Curated(context.Context, int) ([]*skillsv1.ExternalSkill, error) {
	return nil, nil
}
func (f *fakeSkillSearch) Snapshot(_ context.Context, _ string) (*skillsv1.GetSkillSnapshotResponse, error) {
	p := f.path
	if p == "" {
		p = "notes/more.md"
	}
	return &skillsv1.GetSkillSnapshotResponse{Hash: f.hash, Skill: &skillsv1.ExternalSkill{Id: "acme/skills/pdf", Slug: "pdf", Name: "PDF", Source: "acme/skills", Installs: 7, Audits: []*skillsv1.SkillAudit{{Provider: "Socket", Status: "warn", Summary: "One alert"}}},
		Files: []*skillsv1.SkillFile{{Path: "SKILL.md", Contents: "---\ndescription: Do PDFs\nlicense: MIT\n---\nDo them."}, {Path: p, Contents: "more"}}}, nil
}

func listedNames(t *testing.T, s *server.Server, ctx context.Context) []string {
	t.Helper()
	res, err := s.ListSlashCommands(ctx, connect.NewRequest(&agentv1.ListSlashCommandsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range res.Msg.GetCommands() {
		if c.GetKind() == agentv1.SlashCommandKind_SLASH_COMMAND_KIND_SKILL {
			out = append(out, c.GetName())
		}
	}
	return out
}

func TestExternalSkillStaysOutUntilApproved(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, true)
	s := server.New(bootstrap.Options{})
	f := &fakeSkillSearch{hash: "abc"}
	s.SkillSearch = f
	ctx := h.ctx
	home := os.Getenv("HARNESS_HOME")

	peek, err := s.GetExternalSkill(ctx, connect.NewRequest(&agentv1.GetExternalSkillRequest{Id: "acme/skills/pdf"}))
	if err != nil {
		t.Fatal(err)
	}
	if peek.Msg.GetLicense() != "MIT" || peek.Msg.GetDescription() != "Do PDFs" || peek.Msg.GetAudits()[0].GetStatus() != "warn" || peek.Msg.GetInstalled() {
		t.Fatalf("peek %+v", peek.Msg)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "pdf")); !os.IsNotExist(err) {
		t.Fatal("looking stored it")
	}

	if _, err := s.InstallExternalSkill(ctx, connect.NewRequest(&agentv1.InstallExternalSkillRequest{Id: "acme/skills/pdf", Hash: "other"})); err == nil {
		t.Fatal("installed other content than was shown")
	}
	inst, err := s.InstallExternalSkill(ctx, connect.NewRequest(&agentv1.InstallExternalSkillRequest{Id: "acme/skills/pdf", Hash: "abc"}))
	if err != nil {
		t.Fatal(err)
	}
	got := inst.Msg.GetSkill()
	if got.GetApproved() || got.GetPinnedHash() != "abc" || got.GetContentHash() == "" || got.GetAudits()[0].GetStatus() != "warn" || got.GetLicense() != "MIT" {
		t.Fatalf("stored %+v", got)
	}
	if slices.Contains(listedNames(t, s, ctx), "pdf") {
		t.Fatal("an unapproved skill loads")
	}
	list, err := s.ListExtensionSkills(ctx, connect.NewRequest(&agentv1.ListExtensionSkillsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetWaiting()) != 1 || list.Msg.GetWaiting()[0].GetExternal().GetName() != "pdf" {
		t.Fatalf("waiting %+v", list.Msg.GetWaiting())
	}

	if _, err := s.SetExternalSkillApproval(ctx, connect.NewRequest(&agentv1.SetExternalSkillApprovalRequest{Name: "pdf", Approved: true, ContentHash: "sha256:other"})); err == nil {
		t.Fatal("approved other content")
	}
	if _, err := s.SetExternalSkillApproval(ctx, connect.NewRequest(&agentv1.SetExternalSkillApprovalRequest{Name: "pdf", Approved: true, ContentHash: got.GetContentHash()})); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(listedNames(t, s, ctx), "pdf") {
		t.Fatal("an approved skill doesn't load")
	}
	list, err = s.ListExtensionSkills(ctx, connect.NewRequest(&agentv1.ListExtensionSkillsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetWaiting()) != 0 || list.Msg.GetGroups()[0].GetKey() != "external" || !list.Msg.GetGroups()[0].GetSkills()[0].GetExternal().GetApproved() {
		t.Fatalf("after approval %+v", list.Msg)
	}

	// The files change: it stops loading and says so.
	writeFile(t, filepath.Join(home, "skills", "pdf", "SKILL.md"), "---\ndescription: Do something else\n---\nRun this.")
	if slices.Contains(listedNames(t, s, ctx), "pdf") {
		t.Fatal("changed files still load")
	}
	list, err = s.ListExtensionSkills(ctx, connect.NewRequest(&agentv1.ListExtensionSkillsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.GetWaiting()) != 1 || !list.Msg.GetWaiting()[0].GetExternal().GetChangedOnDisk() {
		t.Fatalf("after a change %+v", list.Msg.GetWaiting())
	}

	if _, err := s.RemoveExternalSkill(ctx, connect.NewRequest(&agentv1.RemoveExternalSkillRequest{Name: "pdf"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "pdf")); !os.IsNotExist(err) {
		t.Fatal("still on disk")
	}
}

func TestExternalSkillCantLeaveItsFolderOrRefuseItsRecord(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, true)
	s := server.New(bootstrap.Options{})
	home := os.Getenv("HARNESS_HOME")
	for _, bad := range []string{"../../evil.md", "/etc/evil", "a/../../evil"} {
		s.SkillSearch = &fakeSkillSearch{hash: "abc", path: bad}
		if _, err := s.InstallExternalSkill(h.ctx, connect.NewRequest(&agentv1.InstallExternalSkillRequest{Id: "acme/skills/pdf", Hash: "abc"})); err == nil {
			t.Fatalf("took the path %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "evil.md")); !os.IsNotExist(err) {
		t.Fatal("wrote outside")
	}
	// A personal skill of the name isn't replaced.
	writeFile(t, filepath.Join(home, "skills", "pdf", "SKILL.md"), "---\ndescription: Mine\n---\nMine.")
	s.SkillSearch = &fakeSkillSearch{hash: "abc"}
	if _, err := s.InstallExternalSkill(h.ctx, connect.NewRequest(&agentv1.InstallExternalSkillRequest{Id: "acme/skills/pdf", Hash: "abc"})); err == nil {
		t.Fatal("replaced a personal skill")
	}
	data, _ := os.ReadFile(filepath.Join(home, "skills", "pdf", "SKILL.md"))
	if !strings.Contains(string(data), "Mine") {
		t.Fatal("a personal skill was changed")
	}
}

func TestExternalChangesAreRefusedFromAnotherMachine(t *testing.T) {
	h := startPlugged(t, (&recording{}).respond, true)
	s := server.New(bootstrap.Options{})
	s.SkillSearch = &fakeSkillSearch{hash: "abc"}
	ctx := server.WithRemoteOrigin(h.ctx)
	if _, err := s.InstallExternalSkill(ctx, connect.NewRequest(&agentv1.InstallExternalSkillRequest{Id: "acme/skills/pdf", Hash: "abc"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("install %v", err)
	}
	if _, err := s.SetExternalSkillApproval(ctx, connect.NewRequest(&agentv1.SetExternalSkillApprovalRequest{Name: "pdf", Approved: true, ContentHash: "x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("approve %v", err)
	}
}
