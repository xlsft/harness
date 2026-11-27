// Package sources holds the readers of the import engine (package importer): one file per tool, each registering an
// importer.Source in init. A reader only finds and parses its tool's files; the engine does everything else. See
// docs/design/import.md for how to add one.
//
// Readers never read the real home or environment: everything comes from importer.Env, so tests use a temporary home.
package sources

import (
	"context"
	"sort"
	"sync"

	"github.com/xlsft/harness/packages/client/importer"
)

var (
	mu       sync.Mutex
	registry []importer.Source
)

// register adds a reader; called from the readers' init functions.
func register(s importer.Source) {
	mu.Lock()
	defer mu.Unlock()
	registry = append(registry, s)
}

// All returns the registered sources: by name, with Any folder last.
func All() []importer.Source {
	mu.Lock()
	out := append([]importer.Source(nil), registry...)
	mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Info(), out[j].Info()
		if (a.ID == FolderID) != (b.ID == FolderID) {
			return b.ID == FolderID
		}
		return a.Name < b.Name
	})
	return out
}

// Get returns the source with id, or nil.
func Get(id string) importer.Source {
	for _, s := range All() {
		if s.Info().ID == id {
			return s
		}
	}
	return nil
}

// reader is the usual shape of a source: fill in what the tool has. A nil user means no user-level files.
type reader struct {
	info     importer.SourceInfo
	detect   func(env importer.Env) bool
	user     func(env importer.Env) []importer.Found
	patterns []importer.FolderPattern
}

func (r *reader) Info() importer.SourceInfo {
	info := r.info
	info.User = r.user != nil
	info.Folder = len(r.patterns) > 0
	return info
}

func (r *reader) Detect(env importer.Env) bool {
	return r.detect != nil && r.detect(env)
}

func (r *reader) ScanUser(_ context.Context, env importer.Env) ([]importer.Found, error) {
	if r.user == nil {
		return nil, nil
	}
	return r.user(env), nil
}

func (r *reader) FolderPatterns() []importer.FolderPattern { return r.patterns }
