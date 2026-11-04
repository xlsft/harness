//go:build darwin

package computeruse

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestDarwinReadOnlySmoke talks to the real helper, read-only: permission state (which does not prompt), the app list
// and the frontmost app. It never clicks, types or captures. Skipped unless HARNESS_COMPUTER_SMOKE=1, with
// HARNESS_COMPUTER_HELPER pointing at a helper built by helper/build.sh when it is not installed.
func TestDarwinReadOnlySmoke(t *testing.T) {
	if os.Getenv("HARNESS_COMPUTER_SMOKE") != "1" {
		t.Skip("set HARNESS_COMPUTER_SMOKE=1 to run against the real helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d := NewDriver()
	perms, err := d.Permissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if perms.Reason != "" {
		t.Fatal(perms.Reason)
	}
	t.Logf("permissions: %+v", perms.Items)
	apps, err := d.Apps(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) == 0 {
		t.Fatal("no running apps")
	}
	front, err := d.Frontmost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d apps, %s (%s) in front", len(apps), front.Name, front.ID)
}
