package machineid

import (
	"regexp"
	"testing"
)

func TestFingerprintIsAStableHexHash(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	a := Fingerprint()
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("fingerprint %q is not a hex SHA-256", a)
	}
	if b := Fingerprint(); a != b {
		t.Fatalf("fingerprint changed: %q then %q", a, b)
	}
}

func TestStoredIDIsKept(t *testing.T) {
	t.Setenv("HARNESS_HOME", t.TempDir())
	a := storedID()
	if a == "" || storedID() != a {
		t.Fatalf("stored id not kept: %q", a)
	}
}
