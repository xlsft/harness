package auth_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/harness/packages/client/auth"
	"github.com/xlsft/harness/packages/client/gateway/connectgw"
	"github.com/xlsft/harness/packages/client/internal/fakegateway"
)

func TestDeviceLoginAndRefresh(t *testing.T) {
	srv, url := fakegateway.Start(t, nil)
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := auth.FileStore{Path: path}
	svc := auth.NewConnectService(url, nil)
	var out bytes.Buffer
	m, grant, err := auth.Login(context.Background(), svc, store, url, auth.DeviceInfo{ClientName: "test"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if grant.User.Email != "test@example.com" || grant.User.Role != "admin" || !strings.Contains(out.String(), "TEST-CODE") {
		t.Fatalf("grant %+v out %q", grant, out.String())
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode: %v %v", st.Mode(), err)
	}
	if tok, _ := m.Token(context.Background()); tok != "hat_test" {
		t.Fatalf("token %q", tok)
	}

	// Proactive refresh: a token expiring within 5 minutes is refreshed.
	m.SetAccess("hat_old", 60)
	if tok, _ := m.Token(context.Background()); tok != "hat_test_1" {
		t.Fatalf("proactive refresh: %q", tok)
	}

	// Refresh on UNAUTHENTICATED: one rejected call, then success after refresh.
	gw := connectgw.New(url, nil, m)
	srv.RejectTokens.Store(1)
	cat, err := gw.Models(context.Background())
	if err != nil || len(cat.Models) != 1 {
		t.Fatalf("models after refresh: %v", err)
	}
	if _, _, refreshes := srv.Stats(); refreshes != 2 {
		t.Fatalf("refreshes = %d, want 2", refreshes)
	}
	me, err := m.Me(context.Background())
	if err != nil || me.Device.ID != "dev_test" {
		t.Fatalf("me: %v", err)
	}
}

func TestRevokedDeviceDeletesCredentials(t *testing.T) {
	_, url := fakegateway.Start(t, nil)
	store := auth.FileStore{Path: filepath.Join(t.TempDir(), "c.json")}
	if err := store.Save(&auth.Credentials{Server: url, DeviceToken: "hdt_revoked"}); err != nil {
		t.Fatal(err)
	}
	m, err := auth.NewManager(auth.NewConnectService(url, nil), store)
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Now() }
	if _, err := m.Token(context.Background()); !errors.Is(err, auth.ErrDeviceRevoked) {
		t.Fatalf("want ErrDeviceRevoked, got %v", err)
	}
	if c, _ := store.Load(); c != nil {
		t.Fatal("credentials must be deleted after revocation")
	}
}
