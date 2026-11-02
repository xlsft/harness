package codes

import "testing"

func TestAllCodesValid(t *testing.T) {
	for c := 1; c <= 5000; c++ {
		if !Valid(c) {
			t.Errorf("code %d: description %q does not end with its code", c, Describe(c))
		}
	}
}
