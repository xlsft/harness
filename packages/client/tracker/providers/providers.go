// Package providers is the registry of tracker and knowledge providers: importing it registers every one. Adding a provider is
// one package under packages/client/tracker/<id> that calls tracker.Register and/or tracker.RegisterKnowledge from init, and one
// line here. docs/design/tracker.md, "Writing a provider".
package providers

import (
	// One line per provider package, sorted.
	_ "github.com/xlsft/harness/packages/client/tracker/youtrack" // YouTrack, YouTrack Knowledge Base
)
