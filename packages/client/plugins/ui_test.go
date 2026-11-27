package plugins_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
	"github.com/xlsft/harness/packages/client/plugins"
)

// uiPlugin is a plugin with a pane, a status-bar item and the capabilities given.
func uiPlugin(caps ...string) map[string]string {
	ui, _ := json.Marshal(map[string]any{
		"panes":        []any{map[string]any{"id": "meter", "title": "Cost", "icon": "chart", "page": "./ui/meter.html"}},
		"statusItems":  []any{map[string]any{"id": "cost", "text": "Cost", "pane": "meter"}},
		"capabilities": caps,
		"network":      []string{"https://api.example.com"},
	})
	return map[string]string{
		".harness-plugin/plugin.json": `{"name":"meter","version":"1.0.0","ui":` + string(ui) + `}`,
		"ui/meter.html":               `<!doctype html><html><head><meta http-equiv="refresh" content="0;url=https://evil.example"><base href="https://evil.example/"><link rel="stylesheet" href="./meter.css"><link rel="prefetch" href="https://evil.example/x"><!-- secret --></head><body><img src="./dot.svg"><script src="./meter.js"></script><script src="https://cdn.example/x.js"></script></body></html>`,
		"ui/meter.css":                "body{color:red}</style><script>alert(1)</script>",
		"ui/meter.js":                 "var s = '</SCRIPT><script>alert(1)</script>';",
		"ui/dot.svg":                  `<svg xmlns="http://www.w3.org/2000/svg"/>`,
	}
}

func TestUIManifestIsReadAndChecked(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, uiPlugin("session.read", "theme.read"))
	p, err := plugins.Load(dir, "meter", "local", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Problems) > 0 {
		t.Fatal(p.Problems)
	}
	u := p.Contents.UI
	if len(u.Panes) != 1 || u.Panes[0].Icon != "chart" || u.Panes[0].Path != filepath.Join(dir, "ui", "meter.html") || len(u.StatusItems) != 1 {
		t.Fatalf("%+v", u)
	}
	if !slices.Equal(u.Asks(), []string{"session.read", "theme.read", "network"}) {
		t.Fatal(u.Asks())
	}

	bad := t.TempDir()
	write(t, bad, map[string]string{
		".harness-plugin/plugin.json": `{"name":"bad","ui":{
			"panes":[{"id":"Up","title":"x","page":"./a.html"},{"id":"out","title":"x","page":"./../a.html"},{"id":"js","title":"x","page":"./a.js"},{"id":"ok","title":"Fine","icon":"Bad Icon!","page":"./a.html"}],
			"statusItems":[{"id":"p","text":"Ask","prompt":"do it"},{"id":"both","text":"x","pane":"ok","prompt":"y"},{"id":"ghost","text":"x","pane":"none"}],
			"capabilities":["session.read","tools.run"],
			"network":["http://plain.example","https://ok.example/","*"]}}`,
		"a.html": "<p>hi</p>",
		"a.js":   "",
	})
	p, err = plugins.Load(bad, "bad", "local", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	u = p.UI
	if len(u.Panes) != 1 || u.Panes[0].ID != "ok" || u.Panes[0].Icon != "" || len(u.StatusItems) != 0 || !slices.Equal(u.Capabilities, []string{"session.read"}) || !slices.Equal(u.Network, []string{"https://ok.example"}) {
		t.Fatalf("%+v", u)
	}
	all := strings.Join(p.Problems, "\n")
	for _, want := range []string{`pane id "Up"`, "escapes the plugin folder", "must be an .html file", "needs the session.prompt capability", "exactly one of pane and prompt", `isn't declared: "none"`, `unknown capability "tools.run"`, `"http://plain.example"`, `"*"`} {
		if !strings.Contains(all, want) {
			t.Errorf("no problem says %q:\n%s", want, all)
		}
	}
}

func TestPaneDocumentInlinesOwnFilesAndKeepsTheCSPFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, uiPlugin("session.read"))
	p, err := plugins.Load(dir, "meter", "local", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, csp, err := plugins.PaneDocument(p, p.UI.Panes[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csp, "connect-src 'none'") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatal(csp)
	}
	head := doc[strings.Index(doc, "<head>")+len("<head>"):]
	if !strings.HasPrefix(head, `<meta charset="utf-8"/><meta http-equiv="Content-Security-Policy" content="`) || !strings.Contains(head[:600], "<!--harness-host-->") {
		t.Fatalf("the CSP and the marker are not first in the head:\n%s", doc)
	}
	if strings.Count(doc, "<!--") != 1 || strings.Contains(doc, "secret") {
		t.Fatalf("comments stayed:\n%s", doc)
	}
	for _, gone := range []string{"refresh", "<base", "prefetch", `src="./meter.js"`, `href="./meter.css"`} {
		if strings.Contains(doc, gone) {
			t.Errorf("%s stayed:\n%s", gone, doc)
		}
	}
	for _, want := range []string{"body{color:red}<\\/style>", `var s = '<\/SCRIPT><script>alert(1)<\/script>';`, `src="data:image/svg+xml;base64,`, `src="https://cdn.example/x.js"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("no %s in:\n%s", want, doc)
		}
	}
	// Parsed again, the document has exactly its own two scripts and one style.
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			count[n.Data]++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if count["script"] != 2 || count["style"] != 1 {
		t.Fatalf("an inlined file opened an element: %v\n%s", count, doc)
	}

	_, csp, _ = plugins.PaneDocument(p, p.UI.Panes[0], p.UI.Network)
	if !strings.Contains(csp, "connect-src https://api.example.com") || !strings.Contains(csp, "img-src data: blob: https://api.example.com") {
		t.Fatal(csp)
	}
}

func TestPaneDocumentRefusesFilesOutsideThePlugin(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, map[string]string{"secret.js": "steal()"})
	dir := t.TempDir()
	files := uiPlugin()
	files["ui/meter.html"] = `<script src="../../` + filepath.Base(outside) + `/secret.js"></script>`
	write(t, dir, files)
	if err := os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(dir, "ui", "link.js")); err != nil {
		t.Fatal(err)
	}
	p, err := plugins.Load(dir, "meter", "local", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plugins.PaneDocument(p, p.UI.Panes[0], nil); err == nil {
		t.Fatal("a file outside the plugin was inlined")
	}
	write(t, dir, map[string]string{"ui/meter.html": `<script src="./link.js"></script>`})
	if _, _, err := plugins.PaneDocument(p, p.UI.Panes[0], nil); err == nil || !strings.Contains(err.Error(), "not a file of the plugin") {
		t.Fatalf("a link out of the plugin was followed: %v", err)
	}
}

func TestUIGrantsNeedTheContentShownAndEndWithIt(t *testing.T) {
	m := newStore(t)
	src := filepath.Join(t.TempDir(), "meter")
	write(t, src, uiPlugin("session.read", "session.prompt"))
	rec, err := m.Install(bg, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetUIGrant(rec.ID, "session.read", true, ""); err == nil {
		t.Fatal("granted without saying what")
	}
	if err := m.SetUIGrant(rec.ID, "files.open", true, rec.Hash); err == nil {
		t.Fatal("granted what the plugin doesn't declare")
	}
	if err := m.SetUIGrant(rec.ID, "tools.run", true, rec.Hash); err == nil {
		t.Fatal("granted a capability that doesn't exist")
	}
	for _, c := range []string{"session.read", "network"} {
		if err := m.SetUIGrant(rec.ID, c, true, rec.Hash); err != nil {
			t.Fatal(err)
		}
	}
	if !m.UIGranted(rec.ID, "session.read") || !m.UIGranted(rec.ID, "network") || m.UIGranted(rec.ID, "session.prompt") {
		t.Fatal("grants")
	}
	in, _ := m.Find(rec.ID, plugins.Place{})
	if len(in.UIGrants) != 3 || !in.UIGrants[0].Granted || in.UIGrants[0].At == "" || in.UIGrants[1].Granted || in.UIGrants[2].Capability != "network" {
		t.Fatalf("%+v", in.UIGrants)
	}
	if err := m.SetUIGrant(rec.ID, "session.read", false, ""); err != nil || m.UIGranted(rec.ID, "session.read") {
		t.Fatal("revoke", err)
	}

	// New content asks again.
	write(t, src, map[string]string{"ui/meter.js": "var changed = 1;"})
	if _, err := m.Update(bg, rec.ID); err != nil {
		t.Fatal(err)
	}
	if m.UIGranted(rec.ID, "network") {
		t.Fatal("a grant outlived the content it was made for")
	}
}

func TestPolicyCanTurnPluginUIOff(t *testing.T) {
	doc := func(content string) *syncv1.Document {
		return &syncv1.Document{Scope: commonv1.Scope_SCOPE_ORG, Content: content}
	}
	pol := plugins.PolicyFromDocs([]*syncv1.Document{doc(`{"disablePluginUiNetwork":true}`), doc(`{"disablePluginUi":false}`)})
	if pol.UIDisabled || !pol.UINetworkDisabled || pol.UIBlocked() != "" {
		t.Fatalf("%+v", pol)
	}
	pol = plugins.PolicyFromDocs([]*syncv1.Document{doc(`{"disablePluginUi":true}`), doc(`{"disablePluginUi":false}`)})
	if !pol.UIDisabled || pol.UIBlocked() == "" {
		t.Fatalf("%+v", pol)
	}

	m := newStore(t)
	m.PolicyOf = func() plugins.Policy { return pol }
	src := filepath.Join(t.TempDir(), "meter")
	write(t, src, uiPlugin("session.read"))
	rec, err := m.Install(bg, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetUIGrant(rec.ID, "session.read", true, rec.Hash); err == nil {
		t.Fatal("granted while policy turns plugin UI off")
	}
	if in, _ := m.Find(rec.ID, plugins.Place{}); in.UIBlocked == "" {
		t.Fatal("Extensions isn't told policy hides the UI")
	}
	m.PolicyOf = func() plugins.Policy { return plugins.Policy{UINetworkDisabled: true} }
	if err := m.SetUIGrant(rec.ID, "network", true, rec.Hash); err == nil {
		t.Fatal("granted network while policy keeps panes off it")
	}
}

// The catalog's example loads without a problem, and its pane builds: what authors copy has to work.
func TestCatalogExampleOfPluginUI(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "catalog", "plugins", "session-cost"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plugins.Load(dir, "session-cost", "harness-catalog", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Problems) > 0 || len(p.UI.Panes) != 1 || len(p.UI.StatusItems) != 1 || !slices.Equal(p.UI.Asks(), []string{"session.read", "theme.read"}) {
		t.Fatalf("%+v %v", p.UI, p.Problems)
	}
	doc, _, err := plugins.PaneDocument(p, p.UI.Panes[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "harness.call('session.read')") || strings.Contains(doc, `src="./meter.js"`) || strings.Contains(doc, "meter.css") {
		t.Fatalf("the example's files were not inlined:\n%s", doc)
	}
}
