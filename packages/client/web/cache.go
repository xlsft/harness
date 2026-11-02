package web

import (
	"sync"
	"time"
)

// Cache keeps fetched pages (already converted to Markdown) in memory per
// URL, so asking another question about the same page does not fetch it
// again. Entries expire after TTL; the cache is bounded by total content size.
type Cache struct {
	// TTL is how long an entry is served (default 15 min).
	TTL time.Duration
	// MaxBytes bounds the summed content size (default 50 MB).
	MaxBytes int

	mu      sync.Mutex
	entries map[string]*cacheEntry
	size    int
	now     func() time.Time
}

type cacheEntry struct {
	page page
	at   time.Time
}

// SharedCache is the process-wide cache, shared by all sessions of a client.
var SharedCache = NewCache()

// NewCache returns an empty cache with the default limits.
func NewCache() *Cache {
	return &Cache{TTL: 15 * time.Minute, MaxBytes: 50 << 20, entries: map[string]*cacheEntry{}, now: time.Now}
}

func (c *Cache) ttl() time.Duration {
	if c.TTL <= 0 {
		return 15 * time.Minute
	}
	return c.TTL
}

// get returns the cached page for key, if still fresh.
func (c *Cache) get(key string) (page, bool) {
	if c == nil {
		return page{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return page{}, false
	}
	if c.now().Sub(e.at) >= c.ttl() {
		c.removeLocked(key)
		return page{}, false
	}
	return e.page, true
}

// put stores p under key, evicting expired and then the oldest entries.
func (c *Cache) put(key string, p page) {
	if c == nil {
		return
	}
	limit := c.MaxBytes
	if limit <= 0 {
		limit = 50 << 20
	}
	n := len(p.content)
	if n > limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*cacheEntry{}
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.removeLocked(key)
	now := c.now()
	for k, e := range c.entries {
		if now.Sub(e.at) >= c.ttl() {
			c.removeLocked(k)
		}
	}
	for c.size+n > limit && len(c.entries) > 0 {
		oldest, at := "", now
		for k, e := range c.entries {
			if !e.at.After(at) {
				oldest, at = k, e.at
			}
		}
		c.removeLocked(oldest)
	}
	c.entries[key] = &cacheEntry{page: p, at: now}
	c.size += n
}

func (c *Cache) removeLocked(key string) {
	if e, ok := c.entries[key]; ok {
		c.size -= len(e.page.content)
		delete(c.entries, key)
	}
}
