package gateway

import (
	"errors"
	"testing"

	"github.com/xlsft/harness/packages/client/llm"
)

// A used-up limit is never retried: the server says quota_exceeded, or the reset is far away.
func TestUsedUpLimitsAreNotRetried(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&llm.APIError{Type: "quota_exceeded", Status: 429}, false},
		{&llm.APIError{Type: "rate_limit", Status: 429, RetryAfterMs: 2 * 60 * 60 * 1000}, false},
		{&llm.APIError{Type: "rate_limit", Status: 429, RetryAfterMs: 3000}, true},
		{&llm.APIError{Type: "overloaded", Status: 529}, true},
		{errors.New("connection reset"), true},
	}
	for _, c := range cases {
		if got := retryable(c.err); got != c.want {
			t.Errorf("retryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
