package settings

import (
	"encoding/json"
	"strings"
)

// The machine keys of the policy schema: they configure this installation rather than sessions, and are read only from
// the machine's own policy (DevicePolicies), never from documents the server delivers, which can't move a client to
// another server or another source of updates.
const (
	KeyServerURL       = "serverUrl"
	KeyDisableUpdates  = "disableUpdates"
	KeyUpdateMirrorURL = "updateMirrorUrl"
)

// Machine is what the machine's administrator configured for this installation.
type Machine struct {
	// ServerURL is the server to sign in to; empty lets the user choose.
	ServerURL string `json:"serverUrl,omitempty"`
	// DisableUpdates turns off the app's and the client's own updates.
	DisableUpdates bool `json:"disableUpdates,omitempty"`
	// UpdateMirrorURL replaces GitHub as the source of updates; empty is GitHub.
	UpdateMirrorURL string `json:"updateMirrorUrl,omitempty"`
	// Origins are the places each value came from, by key.
	Origins map[string]string `json:"origins,omitempty"`
}

// MachineConfig reads the machine keys from the machine's policies. The first source that sets a URL wins (device
// management, then the file, then its drop-ins in name order); turning updates off in any of them stands.
func MachineConfig() Machine {
	return machineFrom(DevicePolicies())
}

func machineFrom(list []DevicePolicy) Machine {
	m := Machine{Origins: map[string]string{}}
	for _, p := range list {
		var raw struct {
			ServerURL       *string `json:"serverUrl"`
			DisableUpdates  *bool   `json:"disableUpdates"`
			UpdateMirrorURL *string `json:"updateMirrorUrl"`
		}
		if json.Unmarshal(p.Content, &raw) != nil {
			continue
		}
		if raw.ServerURL != nil && m.ServerURL == "" {
			if u := strings.TrimRight(strings.TrimSpace(*raw.ServerURL), "/"); u != "" {
				m.ServerURL, m.Origins[KeyServerURL] = u, p.Origin
			}
		}
		if raw.UpdateMirrorURL != nil && m.UpdateMirrorURL == "" {
			if u := strings.TrimRight(strings.TrimSpace(*raw.UpdateMirrorURL), "/"); u != "" {
				m.UpdateMirrorURL, m.Origins[KeyUpdateMirrorURL] = u, p.Origin
			}
		}
		if raw.DisableUpdates != nil && *raw.DisableUpdates && !m.DisableUpdates {
			m.DisableUpdates, m.Origins[KeyDisableUpdates] = true, p.Origin
		}
	}
	if len(m.Origins) == 0 {
		m.Origins = nil
	}
	return m
}
