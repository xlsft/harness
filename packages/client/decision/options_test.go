package decision

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/harness/packages/client/auth"
	decisionv1 "github.com/xlsft/harness/packages/client/gen/harness/decision/v1"
	"github.com/xlsft/harness/packages/client/permissions"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func testLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// noGitStatus is an admin extra deny for a command the built-in rules
// fast-allow.
func noGitStatus(version string) func() (*decisionv1.GetRulesOptionsResponse, error) {
	return func() (*decisionv1.GetRulesOptionsResponse, error) {
		return &decisionv1.GetRulesOptionsResponse{
			RulesVersion: version,
			Options: &decisionv1.RulesOptions{
				ExtraDeny: []*decisionv1.RulePattern{{
					Pattern: `^git status\b`, Reason: "git status is reserved for the release bot",
					Categories: []decisionv1.RiskCategory{decisionv1.RiskCategory_RISK_CATEGORY_OUT_OF_SCOPE},
				}},
				ExtraAllow:        []*decisionv1.RulePattern{{Pattern: `^make deploy-docs$`}},
				ProtectedBranches: []string{"main", "prod/*"},
				DenyAllForcePush:  true,
			},
		}, nil
	}
}

func emptyOptions(version string) func() (*decisionv1.GetRulesOptionsResponse, error) {
	return func() (*decisionv1.GetRulesOptionsResponse, error) {
		return &decisionv1.GetRulesOptionsResponse{RulesVersion: version, Options: &decisionv1.RulesOptions{}}, nil
	}
}

func syncedDecider(c *Client, cachePath, user string, log *slog.Logger) (*Cache, *RulesSync) {
	s := NewRulesSync(c, cachePath, c.BaseURL(), user, log)
	s.Rules.Git = c.Git
	cache := NewWithRulesSync(c, s, log)
	cache.Inner.(*Chain).Git = c.Git
	return cache, s
}

func TestClientGetRulesOptions(t *testing.T) {
	svc := &fakeDecisionService{options: noGitStatus("2.abc")}
	c := startDecisionService(t, svc)
	version, opts, err := c.GetRulesOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != "2.abc" {
		t.Fatalf("version %q", version)
	}
	if len(opts.ExtraDeny) != 1 || opts.ExtraDeny[0].Pattern != `^git status\b` || opts.ExtraDeny[0].Reason == "" ||
		strings.Join(opts.ExtraDeny[0].Categories, ",") != CatOutOfScope {
		t.Fatalf("extra deny %+v", opts.ExtraDeny)
	}
	if strings.Join(opts.ExtraAllow, ",") != "^make deploy-docs$" || strings.Join(opts.ProtectedBranches, ",") != "main,prod/*" || !opts.DenyAllForcePush {
		t.Fatalf("options %+v", opts)
	}
	if svc.auth[0] != "Bearer hat_test" {
		t.Fatalf("auth %q", svc.auth[0])
	}
}

// An admin extra deny blocks a command the local rules would fast-allow:
// with no options known, the first decision fetches them before deciding,
// and the deny is final without asking the server.
func TestRulesSyncExtraDenyBlocksLocally(t *testing.T) {
	svc := &fakeDecisionService{options: noGitStatus("2.v1")}
	c := startDecisionService(t, svc)
	ctx := context.Background()

	// Without the options the built-in rules allow it locally.
	if res, _ := New(c, nil).Decide(ctx, bashReq("git status")); res.Behavior != permissions.Allow {
		t.Fatalf("defaults: %+v", res)
	}

	d, s := syncedDecider(c, filepath.Join(t.TempDir(), "opts.json"), "usr_1", nil)
	res, err := d.Decide(ctx, bashReq("git status"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Behavior != permissions.Deny || res.Provider != ProviderLocalRules || res.Reason != "git status is reserved for the release bot" {
		t.Fatalf("got %+v", res)
	}
	if strings.Join(res.Categories, ",") != CatOutOfScope {
		t.Fatalf("categories %v", res.Categories)
	}
	if n := svc.optionsCalls(); n != 1 {
		t.Fatalf("GetRulesOptions calls %d", n)
	}
	if n := len(svc.requests()); n != 0 {
		t.Fatalf("Decide calls %d: a local deny must not reach the server", n)
	}
	if s.Version() != "2.v1" {
		t.Fatalf("version %q", s.Version())
	}
	// The admin's allow and force-push policy apply locally too.
	if res, _ := d.Decide(ctx, bashReq("make deploy-docs")); res.Behavior != permissions.Allow {
		t.Fatalf("extra allow: %+v", res)
	}
	if res, _ := d.Decide(ctx, bashReq("git push --force origin feature/x")); res.Behavior != permissions.Deny {
		t.Fatalf("deny_all_force_push: %+v", res)
	}
	if n := svc.optionsCalls(); n != 1 {
		t.Fatalf("options refetched without a reason: %d calls", n)
	}
}

// A DecideResponse with another rules_version triggers a refetch; the new
// options apply at once and verdicts cached under the old ones are dropped.
func TestRulesSyncRefetchesOnVersionChange(t *testing.T) {
	var mu sync.Mutex
	serverVersion := "2.v1"
	svc := &fakeDecisionService{}
	svc.options = func() (*decisionv1.GetRulesOptionsResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		if serverVersion == "2.v1" {
			return emptyOptions("2.v1")()
		}
		return noGitStatus(serverVersion)()
	}
	svc.respond = func(*decisionv1.DecideRequest) (*decisionv1.DecideResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		return &decisionv1.DecideResponse{Decision: decisionv1.Decision_DECISION_ASK, Provider: "llm", RulesVersion: serverVersion}, nil
	}
	c := startDecisionService(t, svc)
	ctx := context.Background()
	d, s := syncedDecider(c, "", "usr_1", nil)

	if res, _ := d.Decide(ctx, bashReq("git status")); res.Behavior != permissions.Allow {
		t.Fatalf("v1: %+v", res)
	}
	if d.Len() != 1 {
		t.Fatalf("cache len %d", d.Len())
	}
	// Same version reported: no refetch.
	if _, err := d.Decide(ctx, bashReq("npm install left-pad")); err != nil {
		t.Fatal(err)
	}
	if n := svc.optionsCalls(); n != 1 {
		t.Fatalf("refetched on an unchanged version: %d calls", n)
	}

	// The admin changes the options; the next remote verdict reports it.
	mu.Lock()
	serverVersion = "2.v2"
	mu.Unlock()
	if _, err := d.Decide(ctx, bashReq("npm install left-pad")); err != nil {
		t.Fatal(err)
	}
	if n := svc.optionsCalls(); n != 2 {
		t.Fatalf("GetRulesOptions calls %d, want a refetch", n)
	}
	if s.Version() != "2.v2" {
		t.Fatalf("version %q", s.Version())
	}
	res, _ := d.Decide(ctx, bashReq("git status"))
	if res.Behavior != permissions.Deny || res.Cached {
		t.Fatalf("v2: %+v (a cached v1 verdict must not survive)", res)
	}
}

// Offline the cached options apply; with no cache the defaults apply and a
// warning is logged.
func TestRulesSyncOfflineUsesCache(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc := &fakeDecisionService{options: noGitStatus("2.v1")}
	online := startDecisionService(t, svc)
	path := RulesCachePath(dir, online.BaseURL(), "usr_1")
	_, s := syncedDecider(online, path, "usr_1", nil)
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache file: %v %v", fi, err)
	}

	// The same server, now unreachable.
	dead := httptest.NewServer(nil)
	deadURL := dead.URL
	dead.Close()
	offline := NewClient(deadURL, nil, auth.StaticToken("hat_test"))
	offline.Git = gitOn("feature/auto")
	if err := copyFile(path, RulesCachePath(dir, deadURL, "usr_1"), deadURL, online.BaseURL()); err != nil {
		t.Fatal(err)
	}

	log, buf := testLogger()
	d, s2 := syncedDecider(offline, RulesCachePath(dir, deadURL, "usr_1"), "usr_1", log)
	s2.Start(ctx)
	res, err := d.Decide(ctx, bashReq("git status"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Behavior != permissions.Deny || res.Reason != "git status is reserved for the release bot" {
		t.Fatalf("cached options: %+v", res)
	}
	if !s2.Known() || s2.Version() != "2.v1" {
		t.Fatalf("known %v version %q", s2.Known(), s2.Version())
	}

	// Another user's cache is not used: no options at all, the defaults
	// apply and the client warns once.
	d3, s3 := syncedDecider(offline, RulesCachePath(dir, deadURL, "usr_2"), "usr_2", log)
	s3.Start(ctx)
	for range 2 {
		if res, _ := d3.Decide(ctx, bashReq("git status")); res.Behavior != permissions.Allow {
			t.Fatalf("defaults: %+v", res)
		}
	}
	if s3.Known() {
		t.Fatal("options known without a server or a cache")
	}
	if n := strings.Count(buf.String(), "decision rules options unavailable"); n != 1 {
		t.Fatalf("warnings %d:\n%s", n, buf.String())
	}
}

// copyFile copies a cache entry written for server from to one for server to.
func copyFile(src, dst, to, from string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, bytes.Replace(data, []byte(`"`+from+`"`), []byte(`"`+to+`"`), 1), 0o600)
}

func TestRulesSyncRetriesAfterFailure(t *testing.T) {
	svc := &fakeDecisionService{} // GetRulesOptions unimplemented: every fetch fails
	c := startDecisionService(t, svc)
	ctx := context.Background()
	d, s := syncedDecider(c, "", "usr_1", nil)
	s.RetryAfter = 1 << 62
	for range 3 {
		if res, _ := d.Decide(ctx, bashReq("ls")); res.Behavior != permissions.Allow {
			t.Fatalf("defaults: %+v", res)
		}
	}
	if n := svc.optionsCalls(); n != 1 {
		t.Fatalf("GetRulesOptions calls %d, want 1 within the retry interval", n)
	}
	s.RetryAfter = 1
	d.Reset()
	if _, err := d.Decide(ctx, bashReq("ls -la")); err != nil {
		t.Fatal(err)
	}
	if n := svc.optionsCalls(); n != 2 {
		t.Fatalf("GetRulesOptions calls %d, want a retry", n)
	}
}

func TestRulesCachePathIsPerServerAndUser(t *testing.T) {
	a := RulesCachePath("/c", "https://h.example", "usr_1")
	if a == RulesCachePath("/c", "https://h.example", "usr_2") || a == RulesCachePath("/c", "https://other.example", "usr_1") {
		t.Fatal("cache paths collide")
	}
	if filepath.Dir(a) != "/c" || !strings.HasSuffix(a, ".json") {
		t.Fatalf("path %q", a)
	}
}
