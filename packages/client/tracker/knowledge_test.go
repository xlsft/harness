package tracker

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestKnowledgeBorrowsASignInWithoutCopyingIt(t *testing.T) {
	use(&fakeProvider{articles: []Article{{ID: "A-1", Title: "Deploy", Content: "canary"}}})
	s := testService(t, nil)
	tc := saved(t, s, Connection{Name: "Tracker", Options: map[string]string{"kind": "server"}}, "perm:tracker-secret")
	k, err := s.Knowledge.Save(KnowledgeInput{KnowledgeConnection: KnowledgeConnection{Provider: "fake-kb", Enabled: true, Borrow: tc.ID, Auth: AuthToken, Login: "x", Options: map[string]string{"space": "ENG", "junk": "1"}}, Secret: strPtr("ignored")})
	if err != nil {
		t.Fatal(err)
	}
	if k.Auth != "" || k.Login != "" || k.Name != "Fake KB · Tracker" || k.Options["junk"] != "" || k.Options["space"] != "ENG" {
		t.Fatalf("a borrowing connection keeps no sign-in of its own and only declared options: %+v", k)
	}
	if raw, _ := s.Knowledge.Secrets.Get(k.ID); raw != "" {
		t.Fatal("a borrowing connection must store no secret")
	}
	data, _ := os.ReadFile(s.Knowledge.Path)
	if strings.Contains(string(data), "secret") {
		t.Fatalf("no secret in the plain file: %s", data)
	}
	if !s.Knowledge.HasSecret(k) {
		t.Fatal("the borrowed connection has a secret")
	}
	p := s.KnowledgeConnectionProto(k)
	if p.GetBorrow().GetId() != tc.ID || p.GetBorrowName() != "Tracker" || !p.GetHasSecret() {
		t.Fatalf("%v", p)
	}
	// Resolved at open: the tracker's credentials, its address, its options under the source's own.
	if _, err := s.Knowledge.Open(k); err != nil {
		t.Fatal(err)
	}
	o := lastKnowledgeOpen
	if o.Credentials.Secret != "perm:tracker-secret" || o.BaseURL != tc.BaseURL || o.BorrowedFrom != "fake" || o.Options["kind"] != "server" || o.Options["space"] != "ENG" {
		t.Fatalf("%+v", o)
	}
	// A new token on the tracker is used at once: nothing was copied.
	if _, err := s.Store.Save(Input{Connection: tc, Secret: strPtr("perm:rotated")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knowledge.Open(k); err != nil || lastKnowledgeOpen.Credentials.Secret != "perm:rotated" {
		t.Fatalf("%v %q", err, lastKnowledgeOpen.Credentials.Secret)
	}
	// A disabled or removed tracker connection: the source says so.
	tc.Enabled = false
	if _, err := s.Store.Save(Input{Connection: tc}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knowledge.Open(k); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("%v", err)
	}
	if err := s.Store.Delete(tc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knowledge.Open(k); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("%v", err)
	}
	// Only from providers it declares.
	other, err := s.Store.Save(Input{Connection: Connection{Provider: "other", BaseURL: "o.test", Auth: AuthToken, Enabled: true}, Secret: strPtr("x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Knowledge.Save(KnowledgeInput{KnowledgeConnection: KnowledgeConnection{Provider: "fake-kb", Borrow: other.ID}}); err == nil {
		t.Fatal("borrowing from a provider not in BorrowFrom must be refused")
	}
}

func TestKnowledgeWithItsOwnSignIn(t *testing.T) {
	use(&fakeProvider{})
	s := testService(t, nil)
	if _, err := s.Knowledge.Save(KnowledgeInput{KnowledgeConnection: KnowledgeConnection{Provider: "fake-kb", BaseURL: "kb.test", Auth: AuthPassword}}); err == nil {
		t.Fatal("a method the provider does not offer must be refused")
	}
	k, err := s.Knowledge.Save(KnowledgeInput{KnowledgeConnection: KnowledgeConnection{Provider: "fake-kb", BaseURL: "kb.test", Auth: AuthToken, Enabled: true}, Secret: strPtr("kb-secret")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.ID, "kc_") || k.BaseURL != "https://kb.test" || k.Name != "kb.test" {
		t.Fatalf("%+v", k)
	}
	if raw, _ := s.Store.Secrets.Get(k.ID); raw != "" {
		t.Fatal("a knowledge secret must not land among the trackers'")
	}
	if _, err := s.Knowledge.Open(k); err != nil || lastKnowledgeOpen.Credentials.Secret != "kb-secret" || lastKnowledgeOpen.BorrowedFrom != "" {
		t.Fatalf("%v %+v", err, lastKnowledgeOpen)
	}
	if _, err := s.Knowledge.TestDraft(context.Background(), KnowledgeInput{KnowledgeConnection: KnowledgeConnection{Provider: "fake-kb", BaseURL: "kb.test", Auth: AuthToken}, Secret: strPtr("draft")}); err != nil || lastKnowledgeOpen.Credentials.Secret != "draft" {
		t.Fatalf("%v %+v", err, lastKnowledgeOpen)
	}
	if err := s.Knowledge.Delete(k.ID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.Knowledge.Secrets.Get(k.ID); raw != "" {
		t.Fatal("delete removes the secret")
	}
}

func TestCheckOptions(t *testing.T) {
	fields := []OptionField{{Key: "org_id", Label: "Organization", Required: true}, {Key: "kind", Label: "Kind", AllowedValues: []string{"cloud", "360"}, Default: "cloud"}}
	if _, err := CheckOptions(fields, map[string]string{}); err == nil {
		t.Fatal("required")
	}
	if _, err := CheckOptions(fields, map[string]string{"org_id": "1", "kind": "other"}); err == nil {
		t.Fatal("allowed values")
	}
	got, err := CheckOptions(fields, map[string]string{"org_id": " 1 ", "extra": "x"})
	if err != nil || got["org_id"] != "1" || got["kind"] != "cloud" || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := CheckOptions([]OptionField{{Key: "Bad Key"}}, nil); err == nil {
		t.Fatal("keys are checked")
	}
}

func TestTrackerEmailTokenAndOptions(t *testing.T) {
	use(&fakeProvider{})
	s := testService(t, nil)
	if _, err := s.Store.Save(Input{Connection: Connection{Provider: "fake", BaseURL: "x.test", Auth: AuthEmailToken}, Secret: strPtr("t")}); err == nil {
		t.Fatal("an API token needs the account email")
	}
	c, err := s.Store.Save(Input{Connection: Connection{Provider: "fake", BaseURL: "x.test", Auth: AuthEmailToken, Login: "me@example.test"}, Secret: strPtr("t")})
	if err != nil || c.Options["kind"] != "cloud" {
		t.Fatalf("defaults fill in: %+v %v", c, err)
	}
	if _, err := s.Store.Save(Input{Connection: Connection{Provider: "fake", BaseURL: "x.test", Auth: AuthToken, Options: map[string]string{"kind": "moon"}}, Secret: strPtr("t")}); err == nil {
		t.Fatal("allowed values are checked")
	}
	if p := s.ConnectionProto(c); p.GetOptions()["kind"] != "cloud" || len(p.GetCapabilities().GetOptionFields()) != 1 {
		t.Fatalf("%v", p)
	}
}

func TestMigrateKnowledgeFromTrackerConnections(t *testing.T) {
	use(&fakeProvider{articles: []Article{{ID: "A-1", Title: "Deploy", Content: "canary"}}})
	t.Setenv("HARNESS_KEYRING", "0")
	home := t.TempDir()
	s := New(home, nil)
	// What an older client left: the tracker connection served Knowledge itself.
	legacy := saved(t, s, Connection{Name: "Old", KnowledgeEnabled: true, KnowledgeQuery: "project: H"}, "t")
	saved(t, s, Connection{Name: "Plain"}, "t")

	s = New(home, nil)
	list, err := s.Knowledge.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	k := list[0]
	if k.Provider != "fake-kb" || k.Borrow != legacy.ID || k.Query != "project: H" || k.MigratedFrom != legacy.ID || !k.Enabled {
		t.Fatalf("%+v", k)
	}
	if c, _ := s.Store.Get(legacy.ID); c.KnowledgeEnabled || c.KnowledgeQuery != "" {
		t.Fatalf("the flag is cleared: %+v", c)
	}
	// An id from before the split still reads, and comes back under the source's own id.
	doc, content, err := s.Read(context.Background(), "conn:"+legacy.ID+":A-1")
	if err != nil || content != "canary" || doc.GetId() != "conn:"+k.ID+":A-1" {
		t.Fatalf("%v %q %v", doc, content, err)
	}
	// Once only.
	s = New(home, nil)
	if list, _ := s.Knowledge.List(); len(list) != 1 {
		t.Fatalf("%v", list)
	}
}
