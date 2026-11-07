package sandbox

import "errors"

// stubDriver stands in where there is no driver: it says so, and the frontends show the sandbox as unavailable.
type stubDriver struct {
	platform string
	// reason replaces the general one.
	reason string
}

func (d stubDriver) Info() Info {
	if d.reason != "" {
		return Info{Platform: d.platform, Reason: d.reason}
	}
	return Info{Platform: d.platform, Reason: ErrUnsupported.Error() + " (" + d.platform + ")"}
}

func (d stubDriver) Command(Policy, []string) ([]string, error) {
	if d.reason != "" {
		return nil, errors.New(d.reason)
	}
	return nil, ErrUnsupported
}
