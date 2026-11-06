package bootstrap_test

import "os"

// The hooks of the developer's own settings have no place in a test.
func init() { _ = os.Setenv("HARNESS_NO_HOOKS", "1") }
