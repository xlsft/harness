package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Health is what the server says of itself at GET /api/health.
type Health struct {
	Version string
	// WebURL is the web UI the server serves (its address plus the UI's path), "" when it serves none.
	WebURL string
}

// ReadServerHealth asks the server about itself. It is informative only and must not hold up a frontend's start, so it
// gives up quickly and answers the zero Health on any failure.
func ReadServerHealth(ctx context.Context, client *http.Client, url string) Health {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	base := strings.TrimRight(url, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health", nil)
	if err != nil {
		return Health{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Health{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}
	}
	var health struct {
		Version string `json:"version"`
		// The path of the web UI, "/" in the image; absent or "" when the server serves none.
		Web string `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&health); err != nil {
		return Health{}
	}
	h := Health{Version: health.Version}
	if strings.HasPrefix(health.Web, "/") && !strings.HasPrefix(health.Web, "//") {
		h.WebURL = base + strings.TrimRight(health.Web, "/")
	}
	return h
}

// ReadServerVersion is ReadServerHealth's version alone.
func ReadServerVersion(ctx context.Context, client *http.Client, url string) string {
	return ReadServerHealth(ctx, client, url).Version
}
