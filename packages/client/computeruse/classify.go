package computeruse

import (
	"strings"
)

// Tier is what the model may do in an app. It comes from what the app is, and only from there: neither the model nor a
// frontend sets it, and the user's approval grants an app at its tier.
type Tier int

// Tiers, in increasing power. TierBlocked apps are never controllable and are not offered to the user.
const (
	TierBlocked Tier = iota
	TierRead
	TierClick
	TierFull
)

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

// blockedIDs are never controllable: password managers, and what guards the system's security. Harness's own app is
// here too, so the model can never click its own access card or the Stop button.
var blockedIDs = []string{
	"com.apple.Passwords", "com.apple.keychainaccess", "com.apple.systempreferences", "com.apple.SecurityAgent",
	"com.apple.LocalAuthentication.UIAgent", "com.apple.loginwindow", "com.apple.ScreenSaver.Engine",
	"com.apple.TCCUI", "com.apple.UserAccountUpdater", "com.apple.CoreServicesUIAgent",
	"com.1password.*", "com.agilebits.*", "2BUA8C4S2C.com.1password.*", "com.bitwarden.*", "com.8bit.bitwarden*",
	"com.lastpass.*", "com.dashlane.*", "org.keepassxc.*", "com.keepassium.*", "com.callpod.keeper*", "com.keepersecurity.*",
	"in.sinew.Enpass*", "com.nordpass.*", "com.roboform.*", "com.protonpass.*", "me.proton.pass*",
	"dev.xlsft.harness",
}

// blockedNames match the lowercased app name as substrings.
var blockedNames = []string{
	"1password", "bitwarden", "lastpass", "dashlane", "keepass", "keeper password", "enpass", "nordpass", "roboform",
	"proton pass", "password manager", "passwords", "keychain access", "system settings", "system preferences",
	"harness",
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

// Classify returns the tier of an app.
func Classify(a App) Tier {
	name := strings.ToLower(strings.TrimSpace(a.Name))
	switch {
	case matchesID(a.ID, blockedIDs) || containsName(name, blockedNames):
		return TierBlocked
	case matchesID(a.ID, browserIDs) || matchesName(name, browserNames):
		return TierRead
	case matchesID(a.ID, clickIDs) || matchesName(name, clickNames):
		return TierClick
	}
	return TierFull
}

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
