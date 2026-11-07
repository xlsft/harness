package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DeviceSettingsPath is the file of this device's own settings (harness.agent.v1.DeviceSettings). It stays on the
// machine: nothing in it is synced.
func DeviceSettingsPath() string { return filepath.Join(Home(), "device-settings.json") }

// DeviceImageModel is the device-local image model ("local:<provider>/<model>"), "" when none is set.
func DeviceImageModel() string {
	var f struct {
		ImageModel string `json:"imageModel"`
	}
	if b, err := os.ReadFile(DeviceSettingsPath()); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f.ImageModel
}
