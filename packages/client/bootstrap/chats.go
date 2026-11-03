package bootstrap

import (
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/xlsft/harness/packages/client/config"
)

var isolateMu sync.Mutex

// IsolateChats keeps the private working directories of folderless sessions (config.ChatsDir) out of any
// repository above them (HARNESS_HOME inside a dotfiles repository, say): config.ChatsDir is added to
// GIT_CEILING_DIRECTORIES of this process, which the git commands of the client, its tools and terminals
// inherit. git then stops looking for a repository at a chat directory. Idempotent.
func IsolateChats() {
	isolateMu.Lock()
	defer isolateMu.Unlock()
	dir := config.ChatsDir()
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	const name = "GIT_CEILING_DIRECTORIES"
	cur := os.Getenv(name)
	list := filepath.SplitList(cur)
	if slices.Contains(list, dir) {
		return
	}
	if cur == "" {
		_ = os.Setenv(name, dir)
		return
	}
	_ = os.Setenv(name, cur+string(filepath.ListSeparator)+dir)
}
