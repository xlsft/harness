package browser

import (
	"fmt"
	"strings"

	agentv1 "github.com/xlsft/harness/packages/client/gen/harness/agent/v1"
)

// Viewport is the emulated screen of every tab.
type Viewport struct {
	Preset        string // desktop, tablet, mobile, custom
	Width, Height int
	Scale         float64
	Mobile, Touch bool
}

// Viewport limits.
const (
	minViewport = 200
	maxViewport = 4000
)

// DesktopViewport is the default.
var DesktopViewport = Viewport{Preset: "desktop", Width: 1280, Height: 800, Scale: 1}

// PresetViewport returns a preset ("desktop", "tablet", "mobile") or a custom size ("custom" with width and
// height).
func PresetViewport(preset string, width, height int) (Viewport, error) {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "", "desktop":
		v := DesktopViewport
		if width > 0 && height > 0 {
			v.Width, v.Height = width, height
		}
		return v, checkSize(v)
	case "tablet":
		return Viewport{Preset: "tablet", Width: 768, Height: 1024, Scale: 2, Mobile: true, Touch: true}, nil
	case "mobile":
		return Viewport{Preset: "mobile", Width: 375, Height: 812, Scale: 3, Mobile: true, Touch: true}, nil
	case "custom":
		v := Viewport{Preset: "custom", Width: width, Height: height, Scale: 1}
		return v, checkSize(v)
	}
	return Viewport{}, fmt.Errorf("unknown viewport preset %q (want desktop, tablet, mobile or custom)", preset)
}

func checkSize(v Viewport) error {
	if v.Width < minViewport || v.Height < minViewport || v.Width > maxViewport || v.Height > maxViewport {
		return fmt.Errorf("viewport %d×%d is out of range (%d to %d CSS px per side)", v.Width, v.Height, minViewport, maxViewport)
	}
	return nil
}

func (v Viewport) proto() *agentv1.BrowserViewport {
	p := &agentv1.BrowserViewport{Width: uint32(v.Width), Height: uint32(v.Height), DeviceScaleFactor: v.Scale, Mobile: v.Mobile, Touch: v.Touch}
	switch v.Preset {
	case "desktop":
		p.Preset = agentv1.BrowserViewport_PRESET_DESKTOP
	case "tablet":
		p.Preset = agentv1.BrowserViewport_PRESET_TABLET
	case "mobile":
		p.Preset = agentv1.BrowserViewport_PRESET_MOBILE
	case "custom":
		p.Preset = agentv1.BrowserViewport_PRESET_CUSTOM
	}
	return p
}

// ViewportFromProto converts an RPC viewport.
func ViewportFromProto(p *agentv1.BrowserViewport) (Viewport, error) {
	if p == nil {
		return DesktopViewport, nil
	}
	name := ""
	switch p.GetPreset() {
	case agentv1.BrowserViewport_PRESET_DESKTOP, agentv1.BrowserViewport_PRESET_UNSPECIFIED:
		name = "desktop"
	case agentv1.BrowserViewport_PRESET_TABLET:
		name = "tablet"
	case agentv1.BrowserViewport_PRESET_MOBILE:
		name = "mobile"
	case agentv1.BrowserViewport_PRESET_CUSTOM:
		name = "custom"
	}
	return PresetViewport(name, int(p.GetWidth()), int(p.GetHeight()))
}

// userAgents derives the desktop and mobile user agents from the browser's own one: "HeadlessChrome" becomes
// "Chrome" so sites serve their normal pages.
func userAgentFor(v Viewport, browserUA string) string {
	ua := strings.Replace(browserUA, "HeadlessChrome/", "Chrome/", 1)
	version := "130.0.0.0"
	if i := strings.Index(ua, "Chrome/"); i >= 0 {
		rest := ua[i+len("Chrome/"):]
		if j := strings.IndexByte(rest, ' '); j > 0 {
			version = rest[:j]
		} else if rest != "" {
			version = rest
		}
	}
	switch v.Preset {
	case "mobile":
		return "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + " Mobile Safari/537.36"
	case "tablet":
		return "Mozilla/5.0 (Linux; Android 14; Pixel Tablet) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + " Safari/537.36"
	}
	return ua
}
