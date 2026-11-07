package computeruse

import "context"

// Helper is the native helper program as a service for other packages of the client (the iOS Simulator input). Each
// one is its own process, started on first use and kept running: cancelling a call kills it, which stops what it was
// sending, and the next call starts it again. NewHelper returns nil where there is no helper (every platform but macOS).
type Helper interface {
	Call(ctx context.Context, op string, params map[string]any, out any) error
}
