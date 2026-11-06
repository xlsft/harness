package relay

import "os"

// The hooks of the developer's own ~/.claude/settings.json have no place in a test.
func init() { _ = os.Setenv("HARNESS_NO_HOOKS", "1") }
