package bootstrap

import (
	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/web"
)

// registerWebTools adds WebSearch (the server's SearchService) and WebFetch
// (local fetch, answer from the small-slot model through the gateway). Like
// every tool, they reach the model only when the prompt bundle defines them.
func (s *Session) registerWebTools(o Options) {
	s.Tools.Register(&web.WebSearch{Searcher: o.Search})
	small := ""
	if s.Catalog != nil {
		small = s.Catalog.Slots["small"]
	}
	s.Tools.Register(&web.WebFetch{
		Cache:     web.SharedCache,
		UserAgent: "harness/" + config.Version + " (WebFetch)",
		Summarizer: &web.GatewaySummarizer{
			Gateway: s.Gateway,
			Model: func() string {
				if small != "" {
					return small
				}
				return s.Model
			},
			BundleVersion: func() string {
				if s.Agent != nil {
					if b := s.Agent.Bundle(); b != nil {
						return b.Version
					}
				}
				return ""
			},
		},
	})
}
