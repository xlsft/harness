// Package textkit contains small string helpers used by the CLI.
package textkit

import "strings"

// Reverse returns s with its characters in reverse order.
func Reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

// Initials returns the upper-cased first letter of every word in s.
func Initials(s string) string {
	var sb strings.Builder
	for _, w := range strings.Fields(s) {
		for _, r := range w {
			sb.WriteString(strings.ToUpper(string(r)))
			break
		}
	}
	return sb.String()
}
