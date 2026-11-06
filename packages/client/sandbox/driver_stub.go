package sandbox

// stubDriver stands in on platforms without a driver yet: it says so, and the frontends show the sandbox as unavailable.
type stubDriver struct{ platform string }

func (d stubDriver) Info() Info {
	return Info{Platform: d.platform, Reason: ErrUnsupported.Error() + " (" + d.platform + ")"}
}

func (stubDriver) Command(Policy, []string) ([]string, error) { return nil, ErrUnsupported }
