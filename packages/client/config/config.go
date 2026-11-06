// Package config resolves client-wide paths and identity (version, user agent).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Version is the client version; overridden at build time with
// -ldflags "-X github.com/xlsft/harness/packages/client/config.Version=…".
var Version = "0.0.0-dev"

// Distribution says how this binary is delivered: "desktop" for the app's sidecar (packages/desktop/scripts/
// build-sidecar.sh stamps it), which the app updates; empty otherwise.
var Distribution = ""

// ProtocolVersion is sent as the Harness-Protocol header.
const ProtocolVersion = "1"

// UserAgent returns the User-Agent value required by docs/protocol/README.md.
func UserAgent() string {
	return fmt.Sprintf("harness-client/%s (%s; %s)", Version, runtime.GOOS, runtime.GOARCH)
}

// Home returns the harness data directory: $HARNESS_HOME or ~/.harness.
func Home() string {
	if h := os.Getenv("HARNESS_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".harness"
	}
	return filepath.Join(home, ".harness")
}

// PromptCacheDir is where prompt bundles are cached.
func PromptCacheDir() string { return filepath.Join(Home(), "cache", "prompts") }

// ProjectsDir is the root of session transcripts.
func ProjectsDir() string { return filepath.Join(Home(), "projects") }

// ChatsDir holds the private working directories of sessions started without a folder.
func ChatsDir() string { return filepath.Join(Home(), "chats") }

// ChatDir is the private working directory of a folderless session.
func ChatDir(sessionID string) string { return filepath.Join(ChatsDir(), sessionID) }

// IsChatDir reports whether dir is the private working directory of a folderless session.
func IsChatDir(dir string) bool {
	if dir == "" {
		return false
	}
	return filepath.Dir(filepath.Clean(dir)) == ChatsDir()
}

// CredentialsFile is the fallback credential store.
func CredentialsFile() string { return filepath.Join(Home(), "credentials.json") }

// TempRoot is the root for per-session scratch directories:
// $HARNESS_TMPDIR or the OS temp dir, plus harness-<uid>.
func TempRoot() string {
	base := os.Getenv("HARNESS_TMPDIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, fmt.Sprintf("harness-%d", os.Getuid()))
}
