package computeruse

import (
	"strings"
)

// Tier is what the model may do in an app. It comes from what the app is and the user's active profile (profile.go): the
// model never sets it, and the access card grants an app at the tier the profile gives it.
type Tier int

// Tiers, in increasing power. TierBlocked apps are not controllable and are not offered to the user.
const (
	TierBlocked Tier = iota
	TierRead
	TierClick
	TierFull
)

// MarshalText writes a tier by name in the settings file.
func (t Tier) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText reads a tier by name. An unknown one is blocked: what the file does not clearly allow stays off.
func (t *Tier) UnmarshalText(b []byte) error {
	switch string(b) {
	case "read":
		*t = TierRead
	case "click":
		*t = TierClick
	case "full":
		*t = TierFull
	default:
		*t = TierBlocked
	}
	return nil
}

func (t Tier) String() string {
	switch t {
	case TierRead:
		return "read"
	case TierClick:
		return "click"
	case TierFull:
		return "full"
	}
	return "blocked"
}

// Note says what the tier means, for the model and the access card.
func (t Tier) Note() string {
	switch t {
	case TierRead:
		return "Read-only: screenshots and reading the interface. No clicks or typing, because a browser holds signed-in sites; the Browser tools work in a separate browser."
	case TierClick:
		return "Click only: clicks and scrolling, no typing, keys, right click or drag, because text typed here can run commands."
	case TierFull:
		return "Full: clicks, typing and keys."
	}
	return "Never controllable."
}

// Action is what an action needs from the tier of the app it acts on.
type Action string

// Actions.
const (
	ActionRead       Action = "read"
	ActionActivate   Action = "activate"
	ActionClick      Action = "click"
	ActionScroll     Action = "scroll"
	ActionRightClick Action = "right click"
	ActionModClick   Action = "modifier click"
	ActionDrag       Action = "drag"
	ActionType       Action = "type"
	ActionKey        Action = "key"
	ActionMenu       Action = "menu"
)

// Allows reports whether the tier permits an action.
func (t Tier) Allows(a Action) bool {
	switch a {
	case ActionRead, ActionActivate:
		return t >= TierRead
	case ActionClick, ActionScroll:
		return t >= TierClick
	}
	return t >= TierFull
}

// The classification works on bundle ids where it can and falls back on the name, so that a rebuilt or renamed variant
// of a known kind of app lands in the same tier. An id ending in "*" is a prefix. The ids listed are macOS ones; a
// driver for another platform adds its own (executable names, desktop file ids) to the same tables.

// BlockEntry is one line of the blocklist: apps that are not controllable unless the profile lifts this entry.
type BlockEntry struct {
	ID    string
	Label string
	// Warning is what the user reads before lifting it.
	Warning string
	ids     []string
	names   []string
}

func (e BlockEntry) matches(a App) bool {
	return matchesID(a.ID, e.ids) || containsName(strings.ToLower(strings.TrimSpace(a.Name)), e.names)
}

const passwordWarning = "The agent could read and use every password in it, and text it sees in other apps could steer it there."

// blockEntries is the blocklist: password managers, and what guards the system's security. Harness is not an entry:
// nothing lifts it (isHarness).
var blockEntries = []BlockEntry{
	{ID: "1password", Label: "1Password", Warning: passwordWarning, ids: []string{"com.1password.*", "com.agilebits.*", "2BUA8C4S2C.com.1password.*"}, names: []string{"1password"}},
	{ID: "bitwarden", Label: "Bitwarden", Warning: passwordWarning, ids: []string{"com.bitwarden.*", "com.8bit.bitwarden*"}, names: []string{"bitwarden"}},
	{ID: "lastpass", Label: "LastPass", Warning: passwordWarning, ids: []string{"com.lastpass.*"}, names: []string{"lastpass"}},
	{ID: "dashlane", Label: "Dashlane", Warning: passwordWarning, ids: []string{"com.dashlane.*"}, names: []string{"dashlane"}},
	{ID: "keepass", Label: "KeePass apps", Warning: passwordWarning, ids: []string{"org.keepassxc.*", "com.keepassium.*"}, names: []string{"keepass"}},
	{ID: "keeper", Label: "Keeper", Warning: passwordWarning, ids: []string{"com.callpod.keeper*", "com.keepersecurity.*"}, names: []string{"keeper password"}},
	{ID: "enpass", Label: "Enpass", Warning: passwordWarning, ids: []string{"in.sinew.Enpass*"}, names: []string{"enpass"}},
	{ID: "nordpass", Label: "NordPass", Warning: passwordWarning, ids: []string{"com.nordpass.*"}, names: []string{"nordpass"}},
	{ID: "roboform", Label: "RoboForm", Warning: passwordWarning, ids: []string{"com.roboform.*"}, names: []string{"roboform"}},
	{ID: "protonpass", Label: "Proton Pass", Warning: passwordWarning, ids: []string{"com.protonpass.*", "me.proton.pass*"}, names: []string{"proton pass"}},
	{ID: "password_managers", Label: "Other password managers", Warning: passwordWarning, names: []string{"password manager"}},
	{ID: "apple_passwords", Label: "Passwords (macOS)", Warning: passwordWarning, ids: []string{"com.apple.Passwords"}, names: []string{"passwords"}},
	{ID: "keychain", Label: "Keychain Access", Warning: "The agent could read and use the secrets in your keychain.", ids: []string{"com.apple.keychainaccess"}, names: []string{"keychain access"}},
	{ID: "system_settings", Label: "System Settings", Warning: "The agent could change security and privacy settings, and give itself more permissions.", ids: []string{"com.apple.systempreferences"}, names: []string{"system settings", "system preferences"}},
	{ID: "security_dialogs", Label: "System security dialogs", Warning: "The agent could answer password prompts, permission dialogs and the login window in your place.", ids: []string{"com.apple.SecurityAgent", "com.apple.LocalAuthentication.UIAgent", "com.apple.loginwindow", "com.apple.ScreenSaver.Engine", "com.apple.TCCUI", "com.apple.UserAccountUpdater", "com.apple.CoreServicesUIAgent"}},
}

// BlockEntries returns the blocklist.
func BlockEntries() []BlockEntry { return blockEntries }

func blockEntry(id string) *BlockEntry {
	for i := range blockEntries {
		if blockEntries[i].ID == id {
			return &blockEntries[i]
		}
	}
	return nil
}

// isHarness: Harness's own app is never controllable, so the model can never click its own access card or the Stop
// button. No profile lifts this.
func isHarness(a App) bool {
	return matchesID(a.ID, []string{"dev.xlsft.harness"}) || strings.Contains(strings.ToLower(a.Name), "harness")
}

var browserIDs = []string{
	"com.apple.Safari", "com.apple.SafariTechnologyPreview", "com.google.Chrome*", "org.chromium.Chromium",
	"org.mozilla.*", "com.microsoft.edgemac*", "com.brave.Browser*", "company.thebrowser.*", "com.operasoftware.*",
	"com.vivaldi.Vivaldi*", "app.zen-browser.*", "org.torproject.torbrowser", "com.kagi.kagimacOS*", "ai.perplexity.comet*",
	"com.duckduckgo.macos.browser*", "com.sigmaos.*", "net.waterfox.*", "org.librewolf.*",
}

var browserNames = []string{
	"safari", "chrome", "chromium", "firefox", "microsoft edge", "brave browser", "opera", "vivaldi", "zen browser",
	"tor browser", "orion", "duckduckgo", "arc", "dia",
}

// clickIDs are apps in which typed text can run code: terminals, IDEs, and script runners.
var clickIDs = []string{
	"com.apple.Terminal", "com.googlecode.iterm2", "dev.warp.*", "net.kovidgoyal.kitty", "org.alacritty*", "io.alacritty*",
	"com.mitchellh.ghostty", "co.zeit.hyper", "com.github.wez.wezterm", "org.tabby", "com.raggesilver.BlackBox",
	"com.apple.dt.Xcode", "com.microsoft.VSCode*", "com.vscodium*", "com.todesktop.*", "com.jetbrains.*", "com.google.android.studio*",
	"dev.zed.*", "com.sublimetext.*", "com.sublimemerge", "com.panic.Nova", "org.vim.MacVim", "org.gnu.Emacs", "com.exafunction.windsurf*",
	"com.apple.ScriptEditor*", "com.apple.Automator", "com.apple.shortcuts", "com.apple.ActivityMonitor", "com.apple.Console",
	"com.barebones.bbedit", "com.macromates.TextMate", "com.postmanlabs.mac",
}

var clickNames = []string{
	"terminal", "iterm", "warp", "kitty", "alacritty", "ghostty", "hyper", "wezterm", "tabby", "xcode", "visual studio",
	"vscode", "vscodium", "cursor", "windsurf", "intellij", "pycharm", "webstorm", "goland", "rider", "clion", "phpstorm",
	"rubymine", "datagrip", "android studio", "zed", "sublime text", "nova", "macvim", "emacs", "script editor",
	"automator", "shortcuts", "console", "bbedit", "textmate",
}

func isBrowser(a App) bool {
	return matchesID(a.ID, browserIDs) || matchesName(strings.ToLower(strings.TrimSpace(a.Name)), browserNames)
}

func isTerminal(a App) bool {
	return matchesID(a.ID, clickIDs) || matchesName(strings.ToLower(strings.TrimSpace(a.Name)), clickNames)
}

// Classify returns the tier of an app in the Strict profile.
func Classify(a App) Tier { return StrictProfile().TierFor(a) }

func matchesID(id string, patterns []string) bool {
	if id == "" {
		return false
	}
	id = strings.ToLower(id)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(id, prefix) {
				return true
			}
		} else if id == p {
			return true
		}
	}
	return false
}

// containsName is for the blocklist, where a false positive only costs a refusal.
func containsName(name string, list []string) bool {
	for _, n := range list {
		if strings.Contains(name, n) {
			return true
		}
	}
	return false
}

// matchesName matches whole words of the name, since "arc" must not catch "Archive Utility".
func matchesName(name string, list []string) bool {
	padded := " " + strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' {
			return ' '
		}
		return r
	}, name) + " "
	for _, n := range list {
		if strings.Contains(padded, " "+n+" ") {
			return true
		}
	}
	return false
}
