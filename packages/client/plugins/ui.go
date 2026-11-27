package plugins

// Plugin UI: panes (an HTML page of the plugin's folder, shown in a sandboxed frame) and status-bar items, declared under
// "ui" in the manifest. The panes talk to the app only through a versioned message API, and each capability of it is
// declared here and approved on this device on first use. See docs/design/plugin-ui.md.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The capabilities of the message API, version 1, and network: the origins a pane may reach.
const (
	CapSessionRead   = "session.read"
	CapSessionPrompt = "session.prompt"
	CapFilesOpen     = "files.open"
	CapThemeRead     = "theme.read"
	CapNetwork       = "network"
)

// UICapabilities are the capabilities a manifest can declare, in the order they are shown.
var UICapabilities = []string{CapSessionRead, CapSessionPrompt, CapFilesOpen, CapThemeRead}

// UIAPIVersion is the version of the message API this client speaks.
const UIAPIVersion = 1

// Limits of what a manifest declares.
const (
	maxPanes       = 8
	maxStatusItems = 4
	maxTitle       = 60
	maxItemText    = 32
	maxTooltip     = 200
	maxItemPrompt  = 2000
	maxNetwork     = 16
)

var (
	uiIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	iconRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	originRe = regexp.MustCompile(`^(https|wss)://(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:[0-9]{1,5})?$`)
)

// UIPane is a pane a plugin declares.
type UIPane struct {
	ID, Title, Icon string
	// Page is the path as the manifest gives it; Path the file, absolute.
	Page, Path string
}

// UIStatusItem is a status-bar item. A click opens Pane, or sends Prompt to the session.
type UIStatusItem struct {
	ID, Text, Icon, Tooltip string
	Pane, Prompt            string
}

// UI is what a plugin declares under "ui", checked.
type UI struct {
	Panes        []UIPane
	StatusItems  []UIStatusItem
	Capabilities []string
	Network      []string
}

// Empty reports a plugin without UI.
func (u UI) Empty() bool { return len(u.Panes)+len(u.StatusItems) == 0 }

// Declares reports whether the plugin declared a capability; network counts when it names an origin.
func (u UI) Declares(capability string) bool {
	if capability == CapNetwork {
		return len(u.Network) > 0
	}
	return slices.Contains(u.Capabilities, capability)
}

// Asks lists what can be approved for it: its capabilities, then network.
func (u UI) Asks() []string {
	out := append([]string(nil), u.Capabilities...)
	if len(u.Network) > 0 {
		out = append(out, CapNetwork)
	}
	return out
}

// Pane returns a pane by id.
func (u UI) Pane(id string) (UIPane, bool) {
	for _, p := range u.Panes {
		if p.ID == id {
			return p, true
		}
	}
	return UIPane{}, false
}

// loadUI reads the "ui" field. What breaks a rule is a problem and left out; the rest stands.
func (p *Plugin) loadUI() {
	raw := p.Manifest.UI
	if len(raw) == 0 {
		return
	}
	var in struct {
		Panes []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Icon  string `json:"icon"`
			Page  string `json:"page"`
		} `json:"panes"`
		StatusItems []struct {
			ID      string `json:"id"`
			Text    string `json:"text"`
			Icon    string `json:"icon"`
			Tooltip string `json:"tooltip"`
			Pane    string `json:"pane"`
			Prompt  string `json:"prompt"`
		} `json:"statusItems"`
		Capabilities []string `json:"capabilities"`
		Network      []string `json:"network"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		p.problem("ui: %v", err)
		return
	}
	u := &p.UI
	for _, c := range in.Capabilities {
		switch {
		case !slices.Contains(UICapabilities, c):
			p.problem("ui: unknown capability %q", c)
		case !slices.Contains(u.Capabilities, c):
			u.Capabilities = append(u.Capabilities, c)
		}
	}
	for _, o := range in.Network {
		o = strings.TrimRight(o, "/")
		switch {
		case !originRe.MatchString(o):
			p.problem("ui: network takes origins such as https://api.example.com or https://*.example.com: %q", o)
		case len(u.Network) >= maxNetwork:
			p.problem("ui: at most %d network origins", maxNetwork)
		case !slices.Contains(u.Network, o):
			u.Network = append(u.Network, o)
		}
	}
	seen := map[string]bool{}
	for _, d := range in.Panes {
		switch {
		case len(u.Panes) >= maxPanes:
			p.problem("ui: at most %d panes", maxPanes)
			continue
		case !uiIDRe.MatchString(d.ID) || seen[d.ID]:
			p.problem("ui: pane id %q must be unique, lower case letters, digits and \"-\"", d.ID)
			continue
		case strings.TrimSpace(d.Title) == "" || len([]rune(d.Title)) > maxTitle:
			p.problem("ui: pane %s needs a title of at most %d characters", d.ID, maxTitle)
			continue
		}
		ext := strings.ToLower(filepath.Ext(d.Page))
		if ext != ".html" && ext != ".htm" {
			p.problem("ui: pane %s: the page must be an .html file: %q", d.ID, d.Page)
			continue
		}
		abs, ok := p.resolve(d.Page, "ui pane "+d.ID)
		if !ok {
			continue
		}
		if !isFile(abs) {
			p.problem("ui: pane %s: %s is not a file", d.ID, d.Page)
			continue
		}
		seen[d.ID] = true
		u.Panes = append(u.Panes, UIPane{ID: d.ID, Title: strings.TrimSpace(d.Title), Icon: icon(d.Icon), Page: d.Page, Path: abs})
	}
	items := map[string]bool{}
	for _, d := range in.StatusItems {
		text := strings.TrimSpace(d.Text)
		switch {
		case len(u.StatusItems) >= maxStatusItems:
			p.problem("ui: at most %d status-bar items", maxStatusItems)
		case !uiIDRe.MatchString(d.ID) || items[d.ID]:
			p.problem("ui: status-bar item id %q must be unique, lower case letters, digits and \"-\"", d.ID)
		case text == "" || len([]rune(text)) > maxItemText:
			p.problem("ui: status-bar item %s needs a text of at most %d characters", d.ID, maxItemText)
		case (d.Pane == "") == (d.Prompt == ""):
			p.problem("ui: status-bar item %s must set exactly one of pane and prompt", d.ID)
		case d.Pane != "" && !seen[d.Pane]:
			p.problem("ui: status-bar item %s opens a pane that isn't declared: %q", d.ID, d.Pane)
		case d.Prompt != "" && !u.Declares(CapSessionPrompt):
			p.problem("ui: status-bar item %s sends a prompt, which needs the %s capability", d.ID, CapSessionPrompt)
		case len([]rune(d.Prompt)) > maxItemPrompt:
			p.problem("ui: status-bar item %s: the prompt is longer than %d characters", d.ID, maxItemPrompt)
		default:
			items[d.ID] = true
			tip := strings.TrimSpace(d.Tooltip)
			if len([]rune(tip)) > maxTooltip {
				tip = string([]rune(tip)[:maxTooltip])
			}
			u.StatusItems = append(u.StatusItems, UIStatusItem{ID: d.ID, Text: text, Icon: icon(d.Icon), Tooltip: tip, Pane: d.Pane, Prompt: strings.TrimSpace(d.Prompt)})
		}
	}
	if len(u.Capabilities)+len(u.Network) > 0 && u.Empty() {
		p.problem("ui: capabilities and network are declared, and there is no pane or status-bar item to use them")
	}
}

// icon keeps a name of the app's icon set; anything else is the default.
func icon(s string) string {
	if iconRe.MatchString(s) {
		return s
	}
	return ""
}

// UIGrant is this device's approval of one capability of a plugin's UI, for the content it had then.
type UIGrant struct {
	Hash string `json:"hash"`
	At   string `json:"at"`
}

// UIGrantInfo is a capability as Extensions shows it.
type UIGrantInfo struct {
	Capability string
	Granted    bool
	At         string
}

// uiGranted reports a grant that holds for the record's content.
func (r *Record) uiGranted(capability string) (UIGrant, bool) {
	g, ok := r.UIGrants[capability]
	return g, ok && g.Hash != "" && g.Hash == r.Hash
}

// UIBlocked says why policy hides a plugin's UI, "" when it doesn't.
func (p Policy) UIBlocked() string {
	if p.UIDisabled {
		return "Your organization's policy turns off plugin panes and status-bar items."
	}
	return ""
}

// SetUIGrant approves a capability of a plugin's UI on this device, or takes that back. A grant names the content hash the
// user was shown, and holds only while the plugin has that content: a new version asks again.
func (m *Manager) SetUIGrant(id, capability string, granted bool, hash string) error {
	if capability != CapNetwork && !slices.Contains(UICapabilities, capability) {
		return fmt.Errorf("unknown capability %q", capability)
	}
	if !granted {
		return m.change(id, func(r *Record) error { delete(r.UIGrants, capability); return nil })
	}
	pol := m.Policy()
	if why := pol.UIBlocked(); why != "" {
		return fmt.Errorf("%s", why)
	}
	if capability == CapNetwork && pol.UINetworkDisabled {
		return fmt.Errorf("your organization's policy keeps plugin panes off the network")
	}
	st, err := m.snapshot()
	if err != nil {
		return err
	}
	rec := st.Plugins[id]
	if rec == nil {
		return fmt.Errorf("%s is not installed", id)
	}
	if hash == "" || hash != rec.Hash {
		return fmt.Errorf("%s changed since you looked at it; look again", id)
	}
	p, current, err := m.read(rec)
	if err != nil {
		return err
	}
	if current != rec.Hash {
		return fmt.Errorf("the files of %s no longer match what was installed; install it again", id)
	}
	if !p.UI.Declares(capability) {
		return fmt.Errorf("%s does not declare %s", id, capability)
	}
	return m.change(id, func(r *Record) error {
		if r.Hash != hash {
			return fmt.Errorf("%s changed since you looked at it; look again", id)
		}
		if r.UIGrants == nil {
			r.UIGrants = map[string]UIGrant{}
		}
		r.UIGrants[capability] = UIGrant{Hash: hash, At: now()}
		return nil
	})
}

// UIGranted reports whether this device approved a capability for a plugin's current content.
func (m *Manager) UIGranted(id, capability string) bool {
	st, err := m.snapshot()
	if err != nil {
		return false
	}
	rec := st.Plugins[id]
	if rec == nil {
		return false
	}
	_, ok := rec.uiGranted(capability)
	return ok
}

// uiGrants lists what a plugin's UI asks for and how this device stands on each.
func uiGrants(rec *Record, u UI) []UIGrantInfo {
	var out []UIGrantInfo
	for _, c := range u.Asks() {
		g, ok := rec.uiGranted(c)
		out = append(out, UIGrantInfo{Capability: c, Granted: ok, At: g.At})
	}
	return out
}
