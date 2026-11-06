package extensions

import (
	"bytes"
	"context"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/xlsft/harness/packages/client/plugins"
)

// The built-in marketplace ships with the client, so a first run has something to offer with no network: a copy of the repository's
// catalog/ folder (scripts/sync-catalog.sh keeps it, a test checks it). It goes through the same store, policy and approvals as any
// marketplace; it just can't be removed.
//
//go:embed all:builtin
var builtinFS embed.FS

const builtinDir = "builtin"

var (
	builtinMu   sync.Mutex
	builtinDone = map[string]bool{}
)

// EnsureBuiltin writes the built-in marketplace next to the store and registers it, once per process and store. A policy that refuses
// it leaves it out; that isn't an error anyone can act on here.
func EnsureBuiltin(ctx context.Context, m *plugins.Manager) error {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if builtinDone[m.Root] {
		return nil
	}
	dir := filepath.Join(m.Root, "builtin-catalog")
	if err := writeBuiltin(dir); err != nil {
		return err
	}
	src, err := plugins.ParseSource(dir)
	if err != nil {
		return err
	}
	if why := m.Policy().SourceRefusal(src); why != "" {
		return nil
	}
	rec, err := m.AddMarketplace(ctx, src)
	if err != nil {
		return err
	}
	if err := m.MarkBuiltin(rec.Name); err != nil {
		return err
	}
	builtinDone[m.Root] = true
	return nil
}

// writeBuiltin makes dir hold exactly the embedded files.
func writeBuiltin(dir string) error {
	want := map[string]bool{}
	err := fs.WalkDir(builtinFS, builtinDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(builtinDir, p)
		if err != nil {
			return err
		}
		want[rel] = true
		data, err := builtinFS.ReadFile(p)
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, rel)
		if cur, err := os.ReadFile(dest); err == nil && bytes.Equal(cur, data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if !want[rel] {
			return os.Remove(p)
		}
		return nil
	})
}
