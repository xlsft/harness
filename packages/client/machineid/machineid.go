// Package machineid gives a stable fingerprint of this machine for sign-in: the server reuses the machine's device when it
// signs in again, instead of adding one each time. The fingerprint is a salted hash of the operating system's own machine id
// (IOPlatformUUID on macOS, /etc/machine-id on Linux, MachineGuid on Windows), so the id itself never leaves the machine.
// Where the system has none, a random id kept in the client's home stands in.
package machineid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/config"
)

// salt keeps the fingerprint apart from other software's use of the same machine id.
const salt = "xlsft-harness/machine/v1:"

var (
	once   sync.Once
	cached string
)

// Fingerprint returns a hex SHA-256 of the machine id; "" only when neither the system's id nor a stored one is available.
func Fingerprint() string {
	once.Do(func() {
		id := systemID()
		if id == "" {
			id = storedID()
		}
		if id == "" {
			return
		}
		sum := sha256.Sum256([]byte(salt + id))
		cached = hex.EncodeToString(sum[:])
	})
	return cached
}

var (
	ioregUUID = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)
	regGUID   = regexp.MustCompile(`MachineGuid\s+REG_SZ\s+(\S+)`)
)

func systemID() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return ""
		}
		if m := ioregUUID.FindSubmatch(out); m != nil {
			return string(m[1])
		}
	case "windows":
		out, err := exec.Command("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
		if err != nil {
			return ""
		}
		if m := regGUID.FindSubmatch(out); m != nil {
			return string(m[1])
		}
	default:
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if b, err := os.ReadFile(p); err == nil {
				if id := strings.TrimSpace(string(b)); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

// storedID is a random id kept in the client's home, made on first use.
func storedID() string {
	p := filepath.Join(config.Home(), "machine-id")
	if b, err := os.ReadFile(p); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	id := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(p, []byte(id+"\n"), 0o600); err != nil {
		return ""
	}
	return id
}
