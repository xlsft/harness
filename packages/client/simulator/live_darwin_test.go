//go:build darwin

package simulator

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestThrowawaySimulator runs against the real simctl, on a device it creates and deletes itself: it never touches a
// simulator it did not make. It boots it, takes screenshots, and, when the computer use helper is there
// (HARNESS_COMPUTER_HELPER or next to the client), taps, types and presses Home through SimulatorKit, checking that the
// screen changed. Skipped without Xcode, without an iOS runtime, or in -short mode (booting takes a minute).
func TestThrowawaySimulator(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a simulator")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	m := &Manager{}
	if !m.Available(ctx) {
		t.Skip("no Xcode")
	}
	out, err := m.r.Simctl(ctx, nil, "list", "-j", "runtimes")
	if err != nil {
		t.Fatal(err)
	}
	var rts struct {
		Runtimes []struct {
			Identifier  string `json:"identifier"`
			Platform    string `json:"platform"`
			IsAvailable bool   `json:"isAvailable"`
			Supported   []struct {
				Identifier string `json:"identifier"`
				Family     string `json:"productFamily"`
			} `json:"supportedDeviceTypes"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(out, &rts); err != nil {
		t.Fatal(err)
	}
	runtimeID, deviceType := "", ""
	for _, r := range rts.Runtimes {
		if !r.IsAvailable || (r.Platform != "iOS" && !strings.Contains(r.Identifier, "iOS")) {
			continue
		}
		for _, d := range r.Supported {
			if d.Family == "iPhone" {
				runtimeID, deviceType = r.Identifier, d.Identifier
				break
			}
		}
		if runtimeID != "" {
			break
		}
	}
	if runtimeID == "" {
		t.Skip("no iOS runtime installed (Xcode > Settings > Components)")
	}
	udid, err := m.Create(ctx, "harness-test-"+time.Now().Format("150405"), deviceType, runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		_ = m.Shutdown(c, udid)
		if err := m.Delete(c, udid); err != nil {
			t.Errorf("delete the throwaway simulator %s: %v", udid, err)
		}
	})
	if err := m.Boot(ctx, udid); err != nil {
		t.Fatal(err)
	}
	first, err := m.Capture(ctx, udid)
	if err != nil {
		t.Fatal(err)
	}
	if first.W < 300 || first.H < 600 {
		t.Fatalf("frame %d×%d", first.W, first.H)
	}
	frames := 0
	wctx, stop := context.WithTimeout(ctx, 3*time.Second)
	_ = m.Watch(wctx, udid, 250*time.Millisecond, func(Frame) error { frames++; return nil })
	stop()
	if frames == 0 {
		t.Fatal("Watch sent no frame")
	}
	if os.Getenv("HARNESS_COMPUTER_HELPER") == "" {
		t.Log("HARNESS_COMPUTER_HELPER is not set: input is not tried")
		return
	}
	// Spotlight from the home screen: a swipe down, some typing, then Home. Each must change the screen.
	before, _ := m.Capture(ctx, udid)
	if err := m.Swipe(ctx, udid, Point{0.5, 0.4}, Point{0.5, 0.7}, 300); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := m.Type(ctx, udid, "settings"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	after, _ := m.Capture(ctx, udid)
	if string(after.JPEG) == string(before.JPEG) {
		t.Fatal("the swipe and the typing did not change the screen")
	}
	if err := m.Button(ctx, udid, "home", 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Tap(ctx, udid, Point{0.5, 0.5}, 0); err != nil {
		t.Fatal(err)
	}
}
