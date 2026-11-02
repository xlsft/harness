package codes

import (
	"fmt"
	"strings"
)

// Valid reports whether the description of code ends with the code itself, which is the
// invariant every generated entry follows ("<word>-<word>-NNNN").
func Valid(code int) bool {
	return strings.HasSuffix(Describe(code), fmt.Sprintf("-%04d", code))
}
