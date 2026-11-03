package localmodels

import (
	"context"
	"encoding/json"

	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/llm"
)

// Router is the client's gateway: requests for local model ids go to the Registry, everything else to the
// server's gateway (Core). Models merges the server's catalog with the local models of this machine.
type Router struct {
	Core  gateway.Gateway
	Local *Registry
}

var _ gateway.Gateway = (*Router)(nil)

// Wrap routes local model ids of core's requests to the default registry. It returns core unchanged when local
// models are turned off on this client (HARNESS_NO_LOCAL_MODELS=1) or core is already a Router.
func Wrap(core gateway.Gateway) gateway.Gateway {
	if core == nil || Disabled() {
		return core
	}
	if _, ok := core.(*Router); ok {
		return core
	}
	return &Router{Core: core, Local: Default()}
}

// Stream implements gateway.Gateway.
func (r *Router) Stream(ctx context.Context, req *llm.Request) (gateway.EventStream, error) {
	if IsLocal(req.Model) && r.Local != nil {
		return r.Local.Stream(ctx, req)
	}
	return r.Core.Stream(ctx, req)
}

// Models implements gateway.Gateway: the server's catalog, then this machine's local models. When the server
// is unreachable the local models are still offered.
func (r *Router) Models(ctx context.Context) (*llm.Catalog, error) {
	core, err := r.Core.Models(ctx)
	var local []CatalogModel
	if r.Local != nil {
		local = r.Local.Models(ctx)
	}
	if err != nil && len(local) == 0 {
		return core, err
	}
	out := &llm.Catalog{Slots: map[string]string{}}
	if core != nil {
		out.Models = append(out.Models, core.Models...)
		for k, v := range core.Slots {
			out.Slots[k] = v
		}
	}
	for _, m := range local {
		out.Models = append(out.Models, CatalogEntry(m))
	}
	return out, nil
}

// CatalogEntry converts a local model to a catalog entry (free, provider = the provider's name).
func CatalogEntry(m CatalogModel) llm.Model {
	e := llm.Model{ID: m.ID, DisplayName: m.DisplayName, Provider: m.ProviderName, ContextWindow: m.ContextWindow}
	e.Capabilities.Images = m.Vision != nil && *m.Vision
	e.Capabilities.Thinking = m.Thinking != nil && *m.Thinking
	e.Capabilities.ParallelToolCalls = m.Tools != nil && *m.Tools
	e.Pricing = &struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	}{}
	return e
}

// CountTokens implements gateway.Gateway. Local servers have no token counter: the count is estimated (about
// four characters per token).
func (r *Router) CountTokens(ctx context.Context, req *llm.Request) (*llm.TokenCount, error) {
	if !IsLocal(req.Model) {
		return r.Core.CountTokens(ctx, req)
	}
	return EstimateTokens(req), nil
}

// EstimateTokens estimates the input tokens of a request.
func EstimateTokens(req *llm.Request) *llm.TokenCount {
	n := 0
	for _, b := range req.System {
		n += len(b.Text)
	}
	for _, m := range req.Messages {
		raw, _ := json.Marshal(m.Content)
		n += len(raw)
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	return &llm.TokenCount{InputTokens: (n + 3) / 4, Estimated: true}
}
