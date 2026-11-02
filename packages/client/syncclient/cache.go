package syncclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/xlsft/harness/packages/client/gen/harness/common/v1"
	syncv1 "github.com/xlsft/harness/packages/client/gen/harness/sync/v1"
)

// Event is a cache change delivered to subscribers.
type Event struct {
	// Document is set for created/updated documents.
	Document *syncv1.Document
	// Removed is set for deleted documents (tombstones and documents dropped
	// by a full resync).
	Removed *syncv1.Document
	// Resync is set once after a full resync replaced the cache.
	Resync bool
}

type refKey struct {
	scope      commonv1.Scope
	kind       syncv1.DocumentKind
	key        string
	projectKey string
}

func refOf(d *syncv1.Document) refKey {
	return refKey{d.GetScope(), d.GetKind(), d.GetKey(), d.GetProjectKey()}
}

type cacheState struct {
	// Identity is the server URL + user the cache belongs to; a mismatch
	// on open drops the cache.
	Identity string `json:"identity"`
	Cursor   int64  `json:"cursor"`
	Synced   bool   `json:"synced"`
}

// Cache is the local copy of the caller's document feed. Documents are
// stored one file per document under <dir>/docs/ (protojson) plus
// <dir>/state.json with the cursor. It is safe for concurrent use and
// implements memory.DocSource and settings.DocSource.
type Cache struct {
	dir string

	mu    sync.RWMutex
	state cacheState
	docs  map[string]*syncv1.Document // by id
	byRef map[refKey]string
	subs  map[int]func(Event)
	subID int
}

// OpenCache loads the cache in dir. identity names the server and user
// (e.g. "https://harness.example.com|usr_123"); a cache written for another
// identity is discarded. An empty dir keeps the cache in memory only.
func OpenCache(dir, identity string) (*Cache, error) {
	c := &Cache{dir: dir, docs: map[string]*syncv1.Document{}, byRef: map[refKey]string{}, subs: map[int]func(Event){}}
	if dir == "" {
		c.state.Identity = identity
		return c, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		_ = json.Unmarshal(data, &c.state)
	}
	if c.state.Identity != identity {
		// Different server or user: start over.
		if err := os.RemoveAll(filepath.Join(dir, "docs")); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o700); err != nil {
			return nil, err
		}
		c.state = cacheState{Identity: identity}
		return c, c.saveState()
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "docs"))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, "docs", e.Name()))
		if err != nil {
			continue
		}
		d := &syncv1.Document{}
		if err := protojson.Unmarshal(b, d); err != nil || d.GetId() == "" {
			continue
		}
		c.put(d)
	}
	return c, nil
}

// Dir returns the cache directory.
func (c *Cache) Dir() string { return c.dir }

// Cursor is the highest feed version applied.
func (c *Cache) Cursor() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state.Cursor
}

// Synced reports whether a full catch-up with the server has completed at
// least once.
func (c *Cache) Synced() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state.Synced
}

// Lookup returns the cached document for (scope, kind, key, projectKey).
// The returned message must not be modified.
func (c *Cache) Lookup(scope commonv1.Scope, kind syncv1.DocumentKind, key, projectKey string) (*syncv1.Document, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	id, ok := c.byRef[refKey{scope, kind, key, projectKey}]
	if !ok {
		return nil, false
	}
	return c.docs[id], true
}

// Get returns a cached document by id.
func (c *Cache) Get(id string) (*syncv1.Document, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.docs[id]
	return d, ok
}

// List returns the cached documents of kind (all kinds for UNSPECIFIED),
// ordered by scope, project key and key.
func (c *Cache) List(kind syncv1.DocumentKind) []*syncv1.Document {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*syncv1.Document
	for _, d := range c.docs {
		if kind == syncv1.DocumentKind_DOCUMENT_KIND_UNSPECIFIED || d.GetKind() == kind {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.GetScope() != b.GetScope() {
			return a.GetScope() < b.GetScope()
		}
		if a.GetProjectKey() != b.GetProjectKey() {
			return a.GetProjectKey() < b.GetProjectKey()
		}
		if a.GetKind() != b.GetKind() {
			return a.GetKind() < b.GetKind()
		}
		return a.GetKey() < b.GetKey()
	})
	return out
}

// Len returns the number of cached documents.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.docs)
}

// Subscribe registers fn for cache changes; fn runs synchronously after
// the change is stored and must not call back into a blocking Syncer
// method. The returned func unsubscribes.
func (c *Cache) Subscribe(fn func(Event)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subID++
	id := c.subID
	c.subs[id] = fn
	return func() {
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
	}
}

func (c *Cache) notify(evs []Event) {
	if len(evs) == 0 {
		return
	}
	c.mu.RLock()
	subs := make([]func(Event), 0, len(c.subs))
	for _, fn := range c.subs {
		subs = append(subs, fn)
	}
	c.mu.RUnlock()
	for _, ev := range evs {
		for _, fn := range subs {
			fn(ev)
		}
	}
}

// put stores d in memory (caller holds mu or is in OpenCache).
func (c *Cache) put(d *syncv1.Document) {
	k := refOf(d)
	if old, ok := c.byRef[k]; ok && old != d.GetId() {
		delete(c.docs, old)
	}
	c.docs[d.GetId()] = d
	c.byRef[k] = d.GetId()
}

func (c *Cache) remove(id string) *syncv1.Document {
	d, ok := c.docs[id]
	if !ok {
		return nil
	}
	delete(c.docs, id)
	if c.byRef[refOf(d)] == id {
		delete(c.byRef, refOf(d))
	}
	return d
}

// apply applies one change; it returns the event or nil when the change is
// not newer than the cached state. Caller holds mu.
func (c *Cache) apply(ch *syncv1.Change) (*Event, error) {
	switch v := ch.GetChange().(type) {
	case *syncv1.Change_Document:
		d := v.Document
		if d.GetId() == "" {
			return nil, nil
		}
		if old, ok := c.docs[d.GetId()]; ok && old.GetVersion() >= d.GetVersion() {
			return nil, nil
		}
		d = proto.Clone(d).(*syncv1.Document)
		if old, ok := c.byRef[refOf(d)]; ok && old != d.GetId() {
			_ = c.deleteFile(old)
		}
		c.put(d)
		return &Event{Document: d}, c.writeDoc(d)
	case *syncv1.Change_Tombstone:
		t := v.Tombstone
		id := t.GetId()
		if id == "" {
			// Locate by ref when the tombstone has no id.
			id = c.byRef[refKey{t.GetScope(), t.GetKind(), t.GetKey(), t.GetProjectKey()}]
		}
		old, ok := c.docs[id]
		if !ok || old.GetVersion() > t.GetVersion() {
			return nil, nil
		}
		c.remove(id)
		return &Event{Removed: old}, c.deleteFile(id)
	}
	return nil, nil
}

// Apply applies changes and advances the cursor to cursor (if higher).
// markSynced records that a full catch-up has completed.
func (c *Cache) Apply(changes []*syncv1.Change, cursor int64, markSynced bool) error {
	c.mu.Lock()
	var evs []Event
	var firstErr error
	for _, ch := range changes {
		ev, err := c.apply(ch)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ev != nil {
			evs = append(evs, *ev)
		}
	}
	dirty := false
	if cursor > c.state.Cursor {
		c.state.Cursor = cursor
		dirty = true
	}
	if markSynced && !c.state.Synced {
		c.state.Synced = true
		dirty = true
	}
	if dirty {
		if err := c.saveState(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.mu.Unlock()
	c.notify(evs)
	return firstErr
}

// Replace installs a full snapshot: documents not in docs are dropped and
// the cursor is set to cursor (a resync may move it backwards).
func (c *Cache) Replace(docs []*syncv1.Document, cursor int64) error {
	c.mu.Lock()
	keep := map[string]bool{}
	var evs []Event
	var firstErr error
	for _, d := range docs {
		keep[d.GetId()] = true
		ev, err := c.apply(&syncv1.Change{Change: &syncv1.Change_Document{Document: d}})
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ev != nil {
			evs = append(evs, *ev)
		}
	}
	for id := range c.docs {
		if !keep[id] {
			if old := c.remove(id); old != nil {
				evs = append(evs, Event{Removed: old})
			}
			_ = c.deleteFile(id)
		}
	}
	c.state.Cursor = cursor
	c.state.Synced = true
	if err := c.saveState(); err != nil && firstErr == nil {
		firstErr = err
	}
	c.mu.Unlock()
	c.notify(append(evs, Event{Resync: true}))
	return firstErr
}

func fileName(id string) string {
	ok := id != ""
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			ok = false
			break
		}
	}
	if ok {
		return id + ".json"
	}
	sum := sha256.Sum256([]byte(id))
	return "h-" + hex.EncodeToString(sum[:16]) + ".json"
}

func (c *Cache) writeDoc(d *syncv1.Document) error {
	if c.dir == "" {
		return nil
	}
	b, err := protojson.Marshal(d)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(c.dir, "docs", fileName(d.GetId())), b)
}

func (c *Cache) deleteFile(id string) error {
	if c.dir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(c.dir, "docs", fileName(id)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (c *Cache) saveState() error {
	if c.dir == "" {
		return nil
	}
	b, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(c.dir, "state.json"), b)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
