package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

type fakePutter struct {
	reqs []*syncv1.BatchPutDocumentsRequest
}

func (f *fakePutter) BatchPutDocuments(_ context.Context, req *syncv1.BatchPutDocumentsRequest) (*syncv1.BatchPutDocumentsResponse, error) {
	f.reqs = append(f.reqs, req)
	res := &syncv1.BatchPutDocumentsResponse{}
	for range req.GetItems() {
		res.Results = append(res.Results, &syncv1.BatchPutResult{Result: &syncv1.BatchPutResult_Document{Document: &syncv1.Document{Version: 1}}})
	}
	return res, nil
}

// The user-level import carries what used to be read from ~/.claude directly and has no other way across now.
func TestUserImportRulesWorkflowsHooksAndImports(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, ".claude")
	harness := filepath.Join(root, ".harness")
	writeTree(t, root, map[string]string{
		"shared.md":               "Shared",
		".claude/CLAUDE.md":       "Me @extra.md and @../shared.md and @nothing.md\n",
		".claude/extra.md":        "Extra @CLAUDE.md\n",
		".claude/rules/go/one.md": "---\npaths: \"**/*.go\"\n---\nGo\n",
		".claude/rules/empty.md":  " \n",
		".claude/workflows/w.js":  "export default 1\n",
		".claude/hooks/fmt.sh":    "#!/bin/sh\n",
		".claude/settings.json":   `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"~/.claude/hooks/fmt.sh"},{"type":"command","command":"~/.claude/other.sh"}]}]}}`,
	})
	before := treeHash(t, claude)

	dry, err := Run(context.Background(), Options{ClaudeHome: claude, HarnessHome: harness, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(harness); err == nil {
		t.Fatal("a dry run wrote")
	}
	seen := map[string]Item{}
	for _, it := range dry.Items {
		seen[it.Kind+"/"+it.Key] = it
	}
	if it := seen["workflow/w.js"]; it.Status != StatusPlanned {
		t.Fatalf("%+v", dry.Items)
	}
	if it := seen["rule/empty.md"]; it.Status != StatusSkipped {
		t.Fatalf("%+v", it)
	}
	if SectionOf("rule") != SectionMemory || SectionOf("workflow") != SectionWorkflows || SectionOf("hook-script") != SectionSettings {
		t.Fatal("sections")
	}

	put := &fakePutter{}
	rep, err := Run(context.Background(), Options{ClaudeHome: claude, HarnessHome: harness, Putter: put})
	if err != nil {
		t.Fatal(err)
	}
	if rep.HasErrors() {
		t.Fatalf("%+v", rep.Items)
	}
	if treeHash(t, claude) != before {
		t.Fatal("~/.claude changed")
	}
	docs := map[string]string{}
	for _, b := range put.reqs {
		for _, it := range b.GetItems() {
			docs[it.GetRef().GetKind().String()+"/"+it.GetRef().GetKey()] = it.GetContent()
		}
	}
	mem := docs[syncv1.DocumentKind_DOCUMENT_KIND_MEMORY.String()+"/user"]
	if !strings.Contains(mem, "Me Extra") || !strings.Contains(mem, "@../shared.md") || !strings.Contains(mem, "@nothing.md") || strings.Contains(mem, "@extra.md") || strings.Contains(mem, "@CLAUDE.md") {
		t.Fatalf("memory %q", mem)
	}
	if !strings.Contains(docs[syncv1.DocumentKind_DOCUMENT_KIND_RULE.String()+"/go/one.md"], "Go") {
		t.Fatalf("rules: %v", docs)
	}
	hook := docs[syncv1.DocumentKind_DOCUMENT_KIND_HOOK.String()+"/user"]
	if !strings.Contains(hook, "~/.harness/hooks/fmt.sh") || strings.Contains(hook, "~/.claude/hooks/") {
		t.Fatalf("hook %q", hook)
	}
	var hookNote string
	for _, it := range rep.Items {
		if it.Kind == "hooks" {
			hookNote = it.Note
		}
	}
	if !strings.Contains(hookNote, "~/.claude/other.sh") {
		t.Fatalf("what still names .claude is not said: %q", hookNote)
	}
	for _, f := range []string{"workflows/w.js", "hooks/fmt.sh"} {
		if _, err := os.Stat(filepath.Join(harness, f)); err != nil {
			t.Fatal(err)
		}
	}

	// A second run leaves what is on this device.
	writeTree(t, harness, map[string]string{"workflows/w.js": "mine\n"})
	if _, err := Run(context.Background(), Options{ClaudeHome: claude, HarnessHome: harness, Putter: &fakePutter{}}); err != nil {
		t.Fatal(err)
	}
	if got := readT(t, filepath.Join(harness, "workflows/w.js")); got != "mine\n" {
		t.Fatalf("%q", got)
	}
}
