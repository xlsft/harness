package computeruse

import (
	"fmt"
	"maps"
	"slices"
)

// A Profile is the user's named set of decisions on what computer use may do: a tier per kind of app, tiers for single
// apps, and switches for the rules that are on by default. Everything starts out blocked (Strict, the default), and only
// the user loosens it, in Settings on this device: no tool can change a profile, and the choice is kept with the switch,
// never synced. The Controller reads the active profile on every action, so a change holds from the next one.
//
// What no profile can lift: Harness's own app, whose access card and Stop the model must never reach; the need for a
// grant from the user's access card; and the front-app and hit tests, which make input land where the tier was checked.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// The tier by kind of app.
	Browsers  Tier `json:"browsers"`
	Terminals Tier `json:"terminals"`
	Other     Tier `json:"other"`
	// Apps overrides the tier of single apps, by id. TierBlocked blocks one the kind would allow. It does not reach an
	// app on the blocklist, which needs Lifted, nor Harness itself.
	Apps map[string]Tier `json:"apps,omitempty"`
	// Lifted are the blocklist entries (BlockEntries) the user lifted one by one.
	Lifted []string `json:"lifted,omitempty"`
	// AllowSecureTyping lifts the refusal to type into a secure field.
	AllowSecureTyping bool `json:"allow_secure_typing,omitempty"`
	// AllowRemoteOrigin lifts the rule that turns computer use off for a session whose last message came from another
	// device. The access card can still only be answered at this machine.
	AllowRemoteOrigin bool `json:"allow_remote_origin,omitempty"`
	// AllowSystemShortcuts lifts the refusal of key combinations that reach past the app (blockedCombos), and of the
	// system menu.
	AllowSystemShortcuts bool `json:"allow_system_shortcuts,omitempty"`
	// AllowSettingsEdit lets the file tools and Bash write the computer use settings without the user-only prompt.
	AllowSettingsEdit bool `json:"allow_settings_edit,omitempty"`
}

// Profile ids.
const (
	ProfileStrict   = "strict"
	ProfileStandard = "standard"
	ProfileFull     = "full"
	ProfileCustom   = "custom"
)

// StrictProfile is the default: browsers read-only, terminals and IDEs click-only, every switch on the blocked side.
func StrictProfile() Profile {
	return Profile{ID: ProfileStrict, Name: "Strict", Browsers: TierRead, Terminals: TierClick, Other: TierFull}
}

// StandardProfile lets browsers be clicked and typed in; terminals and IDEs stay click-only.
func StandardProfile() Profile {
	return Profile{ID: ProfileStandard, Name: "Standard", Browsers: TierFull, Terminals: TierClick, Other: TierFull}
}

// FullProfile gives the full tier to every kind of app. The blocklist and the switches stay as in Strict: lifting those
// is a choice made one by one, in the custom profile.
func FullProfile() Profile {
	return Profile{ID: ProfileFull, Name: "Full", Browsers: TierFull, Terminals: TierFull, Other: TierFull}
}

// defaultCustom is what the custom profile starts from.
func defaultCustom() Profile {
	p := StrictProfile()
	p.ID, p.Name = ProfileCustom, "Custom"
	return p
}

// normalized returns the custom profile as it is stored and used: its identity fixed, entries that do not exist dropped.
func (p Profile) normalized() Profile {
	p.ID, p.Name = ProfileCustom, "Custom"
	p.Apps = maps.Clone(p.Apps)
	maps.DeleteFunc(p.Apps, func(id string, _ Tier) bool { return id == "" || isHarness(App{ID: id}) })
	if len(p.Apps) == 0 {
		p.Apps = nil
	}
	var lifted []string
	for _, id := range p.Lifted {
		if blockEntry(id) != nil && !slices.Contains(lifted, id) {
			lifted = append(lifted, id)
		}
	}
	p.Lifted = lifted
	return p
}

// TierFor returns the tier the profile gives an app. It is the only place that decides it.
func (p Profile) TierFor(a App) Tier {
	if isHarness(a) {
		return TierBlocked
	}
	for _, e := range blockEntries {
		if e.matches(a) && !slices.Contains(p.Lifted, e.ID) {
			return TierBlocked
		}
	}
	if t, ok := p.Apps[a.ID]; ok {
		return t
	}
	switch {
	case isBrowser(a):
		return p.Browsers
	case isTerminal(a):
		return p.Terminals
	}
	return p.Other
}

// LiftedFor returns the blocklist entry the profile lifted for an app, if any, for the warning on the access card.
func (p Profile) LiftedFor(a App) *BlockEntry {
	for i := range blockEntries {
		if blockEntries[i].matches(a) && slices.Contains(p.Lifted, blockEntries[i].ID) {
			return &blockEntries[i]
		}
	}
	return nil
}

// TierNote says what an app's tier means, for the model and the access card.
func TierNote(p Profile, a App) string {
	t := p.TierFor(a)
	note := t.Note()
	switch {
	case t == TierRead && !isBrowser(a):
		note = "Read-only: screenshots and reading the interface. No clicks or typing."
	case t == TierClick && !isTerminal(a):
		note = "Click only: clicks and scrolling, no typing, keys, right click or drag."
	}
	if e := p.LiftedFor(a); e != nil && t != TierBlocked {
		note = fmt.Sprintf("Normally blocked: %s %s", e.Warning, note)
	}
	return note
}
