package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/harness/packages/client/tools"
)

// withProfile returns a machine with the given active profile, and a session holding grants for the given apps. d.front
// is set to the first app, where input goes.
func withProfile(t *testing.T, p Profile, grant ...string) (*fakeDriver, *Manager, *Controller) {
	t.Helper()
	d := newFake()
	m := &Manager{Driver: d}
	if err := m.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if p.ID == ProfileCustom {
		if err := m.SetProfile(ProfileCustom, &p); err != nil {
			t.Fatal(err)
		}
	} else if err := m.SetProfile(p.ID, nil); err != nil {
		t.Fatal(err)
	}
	c := m.Session("s1")
	if len(grant) > 0 {
		d.front = grant[0]
		d.focused.AppID = grant[0]
		if _, err := c.RequestAccess(context.Background(), &answerer{approve: approveAll}, "tu", "", grant); err != nil {
			t.Fatal(err)
		}
	}
	return d, m, c
}

func custom(mod func(*Profile)) Profile {
	p := defaultCustom()
	mod(&p)
	return p
}

// Everything the tiers decide, by profile and kind of app.
func TestProfilesAndTheirTiers(t *testing.T) {
	apps := map[string]App{
		"browser":  {ID: safari, Name: "Safari"},
		"terminal": {ID: terminal, Name: "Terminal"},
		"other":    {ID: notes, Name: "Notes"},
		"vault":    {ID: vault, Name: "1Password"},
		"settings": {ID: settings, Name: "System Settings"},
		"harness":  {ID: "dev.xlsft.harness", Name: "xlsft`s harness"},
	}
	cases := []struct {
		p    Profile
		want map[string]Tier
	}{
		{StrictProfile(), map[string]Tier{"browser": TierRead, "terminal": TierClick, "other": TierFull, "vault": TierBlocked, "settings": TierBlocked, "harness": TierBlocked}},
		{StandardProfile(), map[string]Tier{"browser": TierFull, "terminal": TierClick, "other": TierFull, "vault": TierBlocked, "settings": TierBlocked, "harness": TierBlocked}},
		{FullProfile(), map[string]Tier{"browser": TierFull, "terminal": TierFull, "other": TierFull, "vault": TierBlocked, "settings": TierBlocked, "harness": TierBlocked}},
		{defaultCustom(), map[string]Tier{"browser": TierRead, "terminal": TierClick, "other": TierFull, "vault": TierBlocked, "settings": TierBlocked, "harness": TierBlocked}},
	}
	for _, tc := range cases {
		for name, a := range apps {
			if got := tc.p.TierFor(a); got != tc.want[name] {
				t.Errorf("%s: %s is %s, want %s", tc.p.ID, name, got, tc.want[name])
			}
		}
	}
}

func TestStrictIsTheDefault(t *testing.T) {
	m := &Manager{Driver: newFake(), Dir: t.TempDir()}
	if m.Profile().ID != ProfileStrict {
		t.Fatalf("%+v", m.Profile())
	}
	st := StrictProfile()
	if st.AllowSecureTyping || st.AllowRemoteOrigin || st.AllowSystemShortcuts || st.AllowSettingsEdit || len(st.Lifted) != 0 || len(st.Apps) != 0 {
		t.Fatalf("Strict loosens something: %+v", st)
	}
	for _, p := range []Profile{StandardProfile(), FullProfile()} {
		if p.AllowSecureTyping || p.AllowRemoteOrigin || p.AllowSystemShortcuts || p.AllowSettingsEdit || len(p.Lifted) != 0 {
			t.Fatalf("%s lifts a switch: %+v", p.ID, p)
		}
	}
	if dir, protected := (&Manager{Driver: newFake(), Dir: "/x"}).SettingsGuard(); dir != "/x" || !protected {
		t.Fatalf("%s %v", dir, protected)
	}
}

func TestStandardLetsABrowserClickAndType(t *testing.T) {
	ctx := context.Background()
	d, _, c := withProfile(t, StandardProfile(), safari, terminal)
	shoot(t, c, safari)
	if _, err := c.Click(ctx, ClickRequest{App: safari, X: 1, Y: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Type(ctx, safari, "hello"); err != nil {
		t.Fatal(err)
	}
	if len(d.clicks) != 1 || len(d.typed) != 1 {
		t.Fatalf("%+v %+v", d.clicks, d.typed)
	}
	d.typed = nil
	_, err := c.Type(ctx, terminal, "ls")
	if err == nil || !strings.Contains(err.Error(), "click-only") || len(d.typed) != 0 {
		t.Fatalf("%v %+v", err, d.typed)
	}
}

func TestFullTypesInATerminalButKeepsTheBlocklist(t *testing.T) {
	ctx := context.Background()
	d, _, c := withProfile(t, FullProfile(), terminal)
	if _, err := c.Type(ctx, terminal, "ls"); err != nil {
		t.Fatal(err)
	}
	a := &answerer{approve: approveAll}
	res, err := c.RequestAccess(ctx, a, "tu", "", []string{vault, settings})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.asked) != 0 || len(res.Blocked) != 2 {
		t.Fatalf("%+v %+v", a.asked, res)
	}
	// Full keeps the other switches too.
	d.focused.Secure = true
	if _, err := c.Type(ctx, terminal, "x"); !errors.Is(err, ErrSecureField) {
		t.Fatalf("%v", err)
	}
	d.focused.Secure = false
	if _, err := c.Key(ctx, terminal, "cmd+tab", 1); err == nil {
		t.Fatal("a system shortcut went through")
	}
}

func TestCustomKindTiersAndAppOverrides(t *testing.T) {
	p := custom(func(p *Profile) {
		p.Terminals = TierRead
		p.Other = TierClick
		p.Browsers = TierBlocked
		p.Apps = map[string]Tier{finder: TierFull, notes: TierBlocked, "com.apple.Chess": TierRead}
	})
	for id, want := range map[string]Tier{safari: TierBlocked, terminal: TierRead, finder: TierFull, notes: TierBlocked, "com.apple.Chess": TierRead, "com.x.other": TierClick} {
		if got := p.TierFor(App{ID: id}); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	ctx := context.Background()
	d, _, c := withProfile(t, p)
	res, err := c.RequestAccess(ctx, &answerer{approve: approveAll}, "tu", "", []string{safari, notes, finder})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocked) != 2 || len(res.Granted) != 1 || res.Granted[0].App.ID != finder {
		t.Fatalf("%+v", res)
	}
	d.front, d.focused.AppID = finder, finder
	if _, err := c.Type(ctx, finder, "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestAppOverrideDoesNotPassTheBlocklist(t *testing.T) {
	p := custom(func(p *Profile) { p.Apps = map[string]Tier{vault: TierFull} })
	if got := p.TierFor(App{ID: vault, Name: "1Password"}); got != TierBlocked {
		t.Fatal(got)
	}
}

func TestBlocklistEntriesLiftOneByOne(t *testing.T) {
	ctx := context.Background()
	d, _, c := withProfile(t, custom(func(p *Profile) { p.Lifted = []string{"1password"} }))
	a := &answerer{approve: approveAll}
	res, err := c.RequestAccess(ctx, a, "tu", "", []string{vault, settings})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Granted) != 1 || res.Granted[0].App.ID != vault || len(res.Blocked) != 1 || res.Blocked[0].ID != settings {
		t.Fatalf("%+v", res)
	}
	// The card still asks, and warns.
	if len(a.asked) != 1 || a.asked[0].Apps[0].Tier != TierFull || !strings.Contains(a.asked[0].Apps[0].Note, "password") {
		t.Fatalf("%+v", a.asked)
	}
	d.front, d.focused.AppID = vault, vault
	if _, err := c.Type(ctx, vault, "x"); err != nil {
		t.Fatal(err)
	}
	// Every entry has its warning, and is its own switch.
	seen := map[string]bool{}
	for _, e := range BlockEntries() {
		if e.Label == "" || e.Warning == "" || seen[e.ID] {
			t.Errorf("%+v", e)
		}
		seen[e.ID] = true
	}
	for _, s := range []struct{ entry, id, name string }{
		{"system_settings", settings, "System Settings"}, {"keychain", "com.apple.keychainaccess", "Keychain Access"},
		{"apple_passwords", "com.apple.Passwords", "Passwords"}, {"security_dialogs", "com.apple.SecurityAgent", "SecurityAgent"},
	} {
		lifted := custom(func(p *Profile) { p.Lifted = []string{s.entry} })
		if got := lifted.TierFor(App{ID: s.id, Name: s.name}); got != TierFull {
			t.Errorf("%s lifted: %s", s.entry, got)
		}
		if got := StrictProfile().TierFor(App{ID: s.id, Name: s.name}); got != TierBlocked {
			t.Errorf("%s in Strict: %s", s.entry, got)
		}
	}
}

// Whatever the profile, the model never reaches Harness's card or Stop.
func TestHarnessCannotBeLifted(t *testing.T) {
	var all []string
	for _, e := range BlockEntries() {
		all = append(all, e.ID)
		if e.matches(App{ID: "dev.xlsft.harness", Name: "xlsft`s harness"}) {
			t.Fatalf("Harness is a liftable entry: %s", e.ID)
		}
	}
	p := custom(func(p *Profile) {
		p.Browsers, p.Terminals, p.Other = TierFull, TierFull, TierFull
		p.Lifted = append(all, "harness")
		p.Apps = map[string]Tier{"dev.xlsft.harness": TierFull}
		p.AllowSecureTyping, p.AllowRemoteOrigin, p.AllowSystemShortcuts, p.AllowSettingsEdit = true, true, true, true
	})
	for _, a := range []App{{ID: "dev.xlsft.harness", Name: "xlsft`s harness"}, {ID: "dev.xlsft.harness"}, {ID: "x.y", Name: "Harness Dev"}} {
		if got := p.TierFor(a); got != TierBlocked {
			t.Errorf("%+v: %s", a, got)
		}
	}
	d, _, c := withProfile(t, p)
	d.apps = append(d.apps, App{ID: "dev.xlsft.harness", Name: "xlsft`s harness", Running: true})
	a := &answerer{approve: approveAll}
	res, err := c.RequestAccess(context.Background(), a, "tu", "", []string{"dev.xlsft.harness"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.asked) != 0 || len(res.Blocked) != 1 {
		t.Fatalf("%+v %+v", a.asked, res)
	}
	if norm := p.normalized(); len(norm.Apps) != 0 || slices.Contains(norm.Lifted, "harness") {
		t.Fatalf("%+v", norm)
	}
}

func TestSecureTypingSwitch(t *testing.T) {
	ctx := context.Background()
	d, _, c := withProfile(t, custom(func(p *Profile) { p.AllowSecureTyping = true }), notes)
	d.focused.Secure = true
	if _, err := c.Type(ctx, notes, "typed"); err != nil {
		t.Fatal(err)
	}
	if len(d.typed) != 1 || d.typed[0].RefuseSecure {
		t.Fatalf("%+v", d.typed)
	}
	if _, err := c.Key(ctx, notes, "cmd+v", 1); err != nil {
		t.Fatal(err)
	}
	if d.keys[0].RefuseSecure {
		t.Fatalf("%+v", d.keys)
	}
	// Reading stays closed: the switch is about typing.
	shoot(t, c, notes)
	d.elements = []Element{{Role: "AXTextField", Value: "hunter2", Secure: true, Frame: Rect{110, 60, 10, 10}}}
	out, err := c.Find(ctx, notes, "", "", 0)
	if err != nil || out[0].Value != "" {
		t.Fatalf("%v %+v", err, out)
	}
	// It does not move the focus check.
	c.d = &focusElsewhere{fakeDriver: d, app: "com.apple.Spotlight"}
	if _, err := c.Type(ctx, notes, "x"); err == nil || !strings.Contains(err.Error(), "keyboard focus") {
		t.Fatalf("%v", err)
	}
}

func TestRemoteOriginSwitch(t *testing.T) {
	ctx := context.Background()
	d, _, c := withProfile(t, custom(func(p *Profile) { p.AllowRemoteOrigin = true }), notes)
	shoot(t, c, notes)
	c.SetRemoteOrigin(true)
	if _, err := c.Click(ctx, ClickRequest{App: notes, X: 1, Y: 1}); err != nil {
		t.Fatal(err)
	}
	if len(d.clicks) != 1 {
		t.Fatalf("%+v", d.clicks)
	}
	// The other profiles keep the rule.
	for _, p := range []Profile{StrictProfile(), StandardProfile(), FullProfile()} {
		_, _, c := withProfile(t, p, notes)
		c.SetRemoteOrigin(true)
		if _, err := c.Type(ctx, notes, "x"); !errors.Is(err, ErrRemote) {
			t.Fatalf("%s: %v", p.ID, err)
		}
	}
}

func TestSystemShortcutsSwitch(t *testing.T) {
	ctx := context.Background()
	d, _, c := withProfile(t, custom(func(p *Profile) { p.AllowSystemShortcuts = true }), notes)
	if _, err := c.Key(ctx, notes, "cmd+tab", 1); err != nil {
		t.Fatal(err)
	}
	if d.keys[0].Combo != "cmd+tab" {
		t.Fatalf("%+v", d.keys)
	}
	if _, err := c.Menu(ctx, notes, []string{"Apple", "About This Mac"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Profile{StrictProfile(), StandardProfile(), FullProfile()} {
		_, _, c := withProfile(t, p, notes)
		if _, err := c.Key(ctx, notes, "cmd+tab", 1); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("%s: %v", p.ID, err)
		}
		if _, err := c.Menu(ctx, notes, []string{"Apple", "About This Mac"}); err == nil {
			t.Fatalf("%s: the system menu went through", p.ID)
		}
	}
}

func TestSettingsEditSwitch(t *testing.T) {
	m := &Manager{Driver: newFake(), Dir: t.TempDir()}
	if _, protected := m.SettingsGuard(); !protected {
		t.Fatal("not protected by default")
	}
	c := custom(func(p *Profile) { p.AllowSettingsEdit = true })
	if err := m.SetProfile(ProfileCustom, &c); err != nil {
		t.Fatal(err)
	}
	if _, protected := m.SettingsGuard(); protected {
		t.Fatal("the switch did nothing")
	}
	if err := m.SetProfile(ProfileFull, nil); err != nil {
		t.Fatal(err)
	}
	if _, protected := m.SettingsGuard(); !protected {
		t.Fatal("Full lifted it")
	}
}

// A stricter profile holds from the next action, on grants already given.
func TestProfileChangeHoldsFromTheNextAction(t *testing.T) {
	ctx := context.Background()
	d, m, c := withProfile(t, StandardProfile(), safari)
	if _, err := c.Type(ctx, safari, "a"); err != nil {
		t.Fatal(err)
	}
	d.typed = nil
	if err := m.SetProfile(ProfileStrict, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Type(ctx, safari, "b"); err == nil || !strings.Contains(err.Error(), "read-only") || len(d.typed) != 0 {
		t.Fatalf("%v", err)
	}
	if st := c.State(); len(st.Grants) != 1 || st.Grants[0].Tier != TierRead {
		t.Fatalf("%+v", st)
	}
	blocked := custom(func(p *Profile) { p.Apps = map[string]Tier{safari: TierBlocked} })
	if err := m.SetProfile(ProfileCustom, &blocked); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Screenshot(ctx, safari, 0); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("%v", err)
	}
	if st := c.State(); len(st.Grants) != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestAccessCardShowsTheProfilesTier(t *testing.T) {
	for _, tc := range []struct {
		p    Profile
		want Tier
		name string
	}{{StrictProfile(), TierRead, "Strict"}, {StandardProfile(), TierFull, "Standard"}, {custom(func(p *Profile) { p.Apps = map[string]Tier{safari: TierClick} }), TierClick, "Custom"}} {
		_, _, c := withProfile(t, tc.p)
		a := &answerer{approve: approveAll}
		if _, err := c.RequestAccess(context.Background(), a, "tu", "", []string{safari}); err != nil {
			t.Fatal(err)
		}
		if got := a.asked[0]; got.Apps[0].Tier != tc.want || got.Profile != tc.name || got.Apps[0].Note == "" {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

func TestProfilesPersistOnThisDevice(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{Driver: newFake(), Dir: dir}
	if err := m.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	c := custom(func(p *Profile) {
		p.Browsers = TierClick
		p.Apps = map[string]Tier{finder: TierRead}
		p.Lifted = []string{"keychain", "no-such-entry"}
		p.AllowRemoteOrigin = true
	})
	if err := m.SetProfile(ProfileCustom, &c); err != nil {
		t.Fatal(err)
	}
	again := &Manager{Driver: newFake(), Dir: dir}
	p := again.Profile()
	if !again.Enabled() || p.ID != ProfileCustom || p.Browsers != TierClick || p.Apps[finder] != TierRead || !p.AllowRemoteOrigin || !slices.Equal(p.Lifted, []string{"keychain"}) {
		t.Fatalf("%+v", p)
	}
	// The other profiles stay as shipped, and the custom one is kept while another is active.
	if err := again.SetProfile(ProfileStandard, nil); err != nil {
		t.Fatal(err)
	}
	profiles, active := (&Manager{Driver: newFake(), Dir: dir}).Profiles()
	if active != ProfileStandard || len(profiles) != 4 || profiles[3].Browsers != TierClick {
		t.Fatalf("%s %+v", active, profiles)
	}
	if err := again.SetProfile("everything", nil); err == nil {
		t.Fatal("an unknown profile was accepted")
	}
}

func TestSettingsFileThatSaysNothingClearIsStrict(t *testing.T) {
	for _, body := range []string{`not json`, `{"enabled":true}`, `{"enabled":true,"active_profile":"root"}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if p := (&Manager{Driver: newFake(), Dir: dir}).Profile(); p.ID != ProfileStrict {
			t.Errorf("%q: %+v", body, p)
		}
	}
	// A tier it cannot read is blocked.
	var p Profile
	if err := json.Unmarshal([]byte(`{"browsers":"everything","other":"full"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Browsers != TierBlocked || p.Other != TierFull {
		t.Fatalf("%+v", p)
	}
}

// A running client reads the file once: an edit behind its back changes nothing until it restarts.
func TestRunningClientIgnoresAnEditOfTheFile(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{Driver: newFake(), Dir: dir}
	if m.Enabled() || m.Profile().ID != ProfileStrict {
		t.Fatal("not the defaults")
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"enabled":true,"active_profile":"full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if m.Enabled() || m.Profile().ID != ProfileStrict {
		t.Fatal("the running client took the edit")
	}
}

// No tool of the model can reach a profile or the switch, whatever its input says.
func TestToolsCannotChangeSettings(t *testing.T) {
	_, m, _ := setup(t)
	env := tools.NewEnv(t.TempDir(), t.TempDir())
	env.SessionID = "s1"
	for _, tool := range m.Tools() {
		tool.Run(context.Background(), env, tools.Call{ID: "x", Input: json.RawMessage(`{"enabled":false,"active_profile":"full","profile":"full","custom_profile":{"allow_settings_edit":true},"app":"Notes","apps":["Notes"]}`)})
	}
	if m.Profile().ID != ProfileStrict || !m.Enabled() || m.Profile().AllowSettingsEdit {
		t.Fatalf("%+v", m.Profile())
	}
}
