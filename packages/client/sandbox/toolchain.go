package sandbox

import "path/filepath"

// toolPath is a place a toolchain writes outside the project, and why it may.
type toolPath struct {
	Path string
	Why  string
}

// toolchainPaths is the one list of what toolchains need to write. It holds caches, which a tool rebuilds if they go wrong,
// and deliberately no install locations: ~/go/bin, ~/.cargo/bin or an npm prefix are on the user's PATH, so a program
// written there runs outside the sandbox on the user's next command. A location its tool lets the user move is read from
// the variable that moves it.
//
// A cache is still written by the sandboxed command and later read by unconfined ones. That is the price of builds that do
// not start from nothing each time, and the reason this list stays short.
func toolchainPaths(home string, getenv func(string) string) []toolPath {
	in := func(rel ...string) string { return filepath.Join(append([]string{home}, rel...)...) }
	or := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	gopath := or("GOPATH", in("go"))
	cargo := or("CARGO_HOME", in(".cargo"))
	volta := or("VOLTA_HOME", in(".volta"))
	return []toolPath{
		{or("XDG_CACHE_HOME", in(".cache")), "the general cache location: uv, pip, pre-commit, and Go on Linux"},
		{or("GOCACHE", in("Library", "Caches", "go-build")), "Go's build cache on macOS"},
		{or("GOMODCACHE", filepath.Join(gopath, "pkg", "mod")), "Go's module cache"},
		{filepath.Join(gopath, "pkg", "sumdb"), "Go's checksum database cache"},
		{or("npm_config_cache", in(".npm")), "npm's cache and logs"},
		{in("Library", "pnpm", "store"), "pnpm's content-addressed store"},
		{in(".local", "share", "pnpm", "store"), "pnpm's content-addressed store on Linux"},
		{in("Library", "Caches", "pnpm"), "pnpm's metadata cache"},
		{in("Library", "Caches", "Yarn"), "Yarn's cache"},
		{in("Library", "Caches", "pip"), "pip's cache on macOS"},
		{filepath.Join(cargo, "registry"), "Cargo's downloaded crates"},
		{filepath.Join(cargo, "git"), "Cargo's git dependencies"},
		{filepath.Join(volta, "tmp"), "Volta's shims unpack and lock here on every call of node or npm"},
		{filepath.Join(volta, "log"), "Volta's shims log here"},
		{darwinUserCacheDir(), "the per-user system cache: clang's module cache and xcrun's database, which cgo needs"},
	}
}
