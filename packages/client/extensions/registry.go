package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultRegistryURL is the public MCP registry.
const DefaultRegistryURL = "https://registry.modelcontextprotocol.io"

const (
	registryPageSize = 100
	registryFresh    = 6 * time.Hour
	searchFresh      = 10 * time.Minute
	registryTimeout  = 5 * time.Second
	// After a failure the registry isn't asked again for a while: search asks often, and each try can take the whole timeout.
	registryBackoff  = time.Minute
	registryMaxBytes = 16 << 20
	maxRememberedAsk = 64
)

// RegistryHeader is a header a remote takes.
type RegistryHeader struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"isRequired"`
	Secret      bool   `json:"isSecret"`
}

// RegistryRemote is a URL a server answers on.
type RegistryRemote struct {
	Type    string           `json:"type"`
	URL     string           `json:"url"`
	Headers []RegistryHeader `json:"headers"`
}

// RegistryEnv is an environment variable a package reads.
type RegistryEnv struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"isRequired"`
	Secret      bool   `json:"isSecret"`
}

// RegistryArg is an argument a package takes.
type RegistryArg struct {
	Required bool `json:"isRequired"`
}

// RegistryPackage is a package that runs a server locally.
type RegistryPackage struct {
	RegistryType string `json:"registryType"`
	Identifier   string `json:"identifier"`
	Version      string `json:"version"`
	Transport    struct {
		Type string `json:"type"`
	} `json:"transport"`
	Env []RegistryEnv `json:"environmentVariables"`
	// Required arguments can't be filled in for the user, so such a package isn't offered.
	RuntimeArgs []RegistryArg `json:"runtimeArguments"`
	PackageArgs []RegistryArg `json:"packageArguments"`
}

// RegistryServer is the latest version of a server in the registry.
type RegistryServer struct {
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Website     string            `json:"websiteUrl"`
	Remotes     []RegistryRemote  `json:"remotes"`
	Packages    []RegistryPackage `json:"packages"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	// UpdatedAt comes from the registry's own metadata.
	UpdatedAt string `json:"updatedAt"`
}

// RegistryPage is what the registry answered, or what is left of it when it can't be reached.
type RegistryPage struct {
	Servers    []RegistryServer
	NextCursor string
	FetchedAt  time.Time
	// Problem says the registry couldn't be reached; Servers then come from the saved list.
	Problem string
	// Off: it wasn't asked at all.
	Off bool
}

// Registry reads the public MCP registry through a cache in a file, so Discover opens at once and works offline.
type Registry struct {
	// BaseURL is the registry; "off" stops all requests. Empty: the public one.
	BaseURL string
	// HTTP nil: the default client.
	HTTP *http.Client
	// CachePath is the file of the first page.
	CachePath string
	Now       func() time.Time

	mu       sync.Mutex
	failedAt time.Time
	asked    map[string]askMemo
}

type askMemo struct {
	page RegistryPage
	at   time.Time
}

type registryCache struct {
	FetchedAt  time.Time        `json:"fetchedAt"`
	NextCursor string           `json:"nextCursor"`
	Servers    []RegistryServer `json:"servers"`
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Registry) base() string {
	if r.BaseURL != "" {
		return strings.TrimRight(r.BaseURL, "/")
	}
	return DefaultRegistryURL
}

func (r *Registry) off() bool { return r.BaseURL == "off" }

func (r *Registry) readCache() *registryCache {
	data, err := os.ReadFile(r.CachePath)
	if err != nil {
		return nil
	}
	var c registryCache
	if json.Unmarshal(data, &c) != nil {
		return nil
	}
	return &c
}

func (r *Registry) writeCache(c registryCache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.CachePath), 0o700); err != nil {
		return err
	}
	tmp := r.CachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.CachePath)
}

// Page returns servers. A plain first page comes from the cache while it is fresh, a search or a later page from a short memory. When
// the registry can't be reached the saved list stands in, filtered by the query, with Problem saying so. refresh skips the caches.
func (r *Registry) Page(ctx context.Context, query, cursor string, refresh bool) RegistryPage {
	query = strings.TrimSpace(query)
	if r.off() {
		return r.saved(query, RegistryPage{Off: true})
	}
	first := query == "" && cursor == ""
	key := query + "\x00" + cursor
	if !refresh {
		if first {
			if c := r.readCache(); c != nil && r.now().Sub(c.FetchedAt) < registryFresh {
				return RegistryPage{Servers: c.Servers, NextCursor: c.NextCursor, FetchedAt: c.FetchedAt}
			}
		} else {
			r.mu.Lock()
			m, ok := r.asked[key]
			r.mu.Unlock()
			if ok && r.now().Sub(m.at) < searchFresh {
				return m.page
			}
		}
	}
	r.mu.Lock()
	waiting := !refresh && !r.failedAt.IsZero() && r.now().Sub(r.failedAt) < registryBackoff
	r.mu.Unlock()
	if waiting {
		return r.saved(query, RegistryPage{Problem: "The MCP registry can't be reached right now."})
	}
	page, err := r.fetch(ctx, query, cursor)
	if err != nil {
		// The screen went away: that says nothing about the registry.
		if errors.Is(ctx.Err(), context.Canceled) {
			return r.saved(query, RegistryPage{})
		}
		r.mu.Lock()
		r.failedAt = r.now()
		r.mu.Unlock()
		return r.saved(query, RegistryPage{Problem: "The MCP registry can't be reached right now: " + err.Error()})
	}
	page.FetchedAt = r.now()
	r.mu.Lock()
	r.failedAt = time.Time{}
	r.mu.Unlock()
	if first {
		// A cache that can't be written costs a fetch next time, nothing else.
		_ = r.writeCache(registryCache{FetchedAt: page.FetchedAt, NextCursor: page.NextCursor, Servers: page.Servers})
	} else {
		r.mu.Lock()
		if r.asked == nil || len(r.asked) >= maxRememberedAsk {
			r.asked = map[string]askMemo{}
		}
		r.asked[key] = askMemo{page: page, at: page.FetchedAt}
		r.mu.Unlock()
	}
	return page
}

// saved fills a page from the file, whatever its age.
func (r *Registry) saved(query string, p RegistryPage) RegistryPage {
	c := r.readCache()
	if c == nil {
		return p
	}
	p.FetchedAt = c.FetchedAt
	q := strings.ToLower(query)
	for _, s := range c.Servers {
		if q == "" || strings.Contains(strings.ToLower(s.Name+" "+s.Title+" "+s.Description), q) {
			p.Servers = append(p.Servers, s)
		}
	}
	return p
}

func (r *Registry) fetch(ctx context.Context, query, cursor string) (RegistryPage, error) {
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	q := url.Values{"version": {"latest"}, "limit": {fmt.Sprint(registryPageSize)}}
	if query != "" {
		q.Set("search", query)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base()+"/v0.1/servers?"+q.Encode(), nil)
	if err != nil {
		return RegistryPage{}, err
	}
	req.Header.Set("Accept", "application/json")
	hc := r.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return RegistryPage{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return RegistryPage{}, fmt.Errorf("status %d", res.StatusCode)
	}
	return parseRegistry(io.LimitReader(res.Body, registryMaxBytes))
}

func parseRegistry(body io.Reader) (RegistryPage, error) {
	var doc struct {
		Servers []struct {
			Server RegistryServer `json:"server"`
			Meta   struct {
				Official struct {
					Status    string `json:"status"`
					UpdatedAt string `json:"updatedAt"`
				} `json:"io.modelcontextprotocol.registry/official"`
			} `json:"_meta"`
		} `json:"servers"`
		Metadata struct {
			NextCursor string `json:"nextCursor"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		return RegistryPage{}, fmt.Errorf("its answer can't be read: %w", err)
	}
	p := RegistryPage{NextCursor: doc.Metadata.NextCursor}
	for _, s := range doc.Servers {
		if st := s.Meta.Official.Status; st != "" && st != "active" {
			continue
		}
		srv := s.Server
		srv.UpdatedAt = s.Meta.Official.UpdatedAt
		p.Servers = append(p.Servers, srv)
	}
	return p, nil
}
