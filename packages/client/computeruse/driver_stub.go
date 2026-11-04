package computeruse

import "context"

// stubDriver stands in on platforms without a driver yet: it says so, and the tools are not offered.
type stubDriver struct{ platform string }

func (d stubDriver) Info() Info {
	return Info{Platform: d.platform, Reason: ErrUnsupportedPlatform.Error() + " (" + d.platform + ")", Surface: SurfaceDesktop}
}

func (stubDriver) Permissions(context.Context) (Permissions, error) {
	return Permissions{}, ErrUnsupportedPlatform
}
func (stubDriver) Apps(context.Context, bool) ([]App, error) { return nil, ErrUnsupportedPlatform }
func (stubDriver) Frontmost(context.Context) (App, error)    { return App{}, ErrUnsupportedPlatform }
func (stubDriver) AppAt(context.Context, Point) (App, error) { return App{}, ErrUnsupportedPlatform }
func (stubDriver) Windows(context.Context, string) ([]Window, error) {
	return nil, ErrUnsupportedPlatform
}
func (stubDriver) FocusedElement(context.Context) (FocusedElement, error) {
	return FocusedElement{}, ErrUnsupportedPlatform
}
func (stubDriver) Screenshot(context.Context, ScreenshotRequest) (Image, error) {
	return Image{}, ErrUnsupportedPlatform
}
func (stubDriver) Activate(context.Context, string) error { return ErrUnsupportedPlatform }
func (stubDriver) FocusWindow(context.Context, string, int64) error {
	return ErrUnsupportedPlatform
}
func (stubDriver) Click(context.Context, Click) error   { return ErrUnsupportedPlatform }
func (stubDriver) Drag(context.Context, Drag) error     { return ErrUnsupportedPlatform }
func (stubDriver) Scroll(context.Context, Scroll) error { return ErrUnsupportedPlatform }
func (stubDriver) Type(context.Context, TypeText) error { return ErrUnsupportedPlatform }
func (stubDriver) Key(context.Context, KeyPress) error  { return ErrUnsupportedPlatform }
func (stubDriver) Find(context.Context, FindQuery) ([]Element, error) {
	return nil, ErrUnsupportedPlatform
}
func (stubDriver) Menu(context.Context, string, []string) error { return ErrUnsupportedPlatform }
