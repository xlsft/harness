//go:build darwin

package sandbox

import (
	"os/exec"
	"strings"
	"sync"
)

// darwinUserCacheDir is confstr's _CS_DARWIN_USER_CACHE_DIR, a per-user folder under /var/folders.
var darwinUserCacheDir = sync.OnceValue(func() string {
	out, err := exec.Command("/usr/bin/getconf", "DARWIN_USER_CACHE_DIR").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})
