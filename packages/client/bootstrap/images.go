package bootstrap

import (
	"fmt"

	"github.com/xlsft/harness/packages/client/config"
	"github.com/xlsft/harness/packages/client/gateway"
	"github.com/xlsft/harness/packages/client/images"
	"github.com/xlsft/harness/packages/client/localmodels"
	"github.com/xlsft/harness/packages/client/settings"
	"github.com/xlsft/harness/packages/client/tools"
)

// registerImageGenerate adds the ImageGenerate tool when the user has an image model (docs/design/images.md), in this
// order: the device-local model of this machine, the user's synced choice, the server default. Otherwise the tool stays
// out of the session's list, like Advisor without a model; a warning says why when a chosen model is not available.
func (s *Session) registerImageGenerate(gw gateway.Gateway, st *settings.Settings, disabled map[string]bool) {
	s.Tools.Register(&tools.ImageGenerate{})
	gen, ok := gw.(images.Generator)
	if !ok {
		disabled["ImageGenerate"] = true
		return
	}
	if id := config.DeviceImageModel(); id != "" {
		if r, isRouter := gw.(*localmodels.Router); isRouter && r.Local != nil {
			_, name, err := r.Local.ImageProvider(id)
			if err == nil {
				s.Tools.Register(&tools.ImageGenerate{Generator: gen, Model: id, ModelName: name})
				return
			}
			s.Warnings = append(s.Warnings, fmt.Sprintf("The device-local image model %s cannot be used (%v); the session uses the synced choice.", id, err))
		}
	}
	chosen := ""
	if st != nil {
		chosen = st.ImageModel
	}
	if chosen != "" && !localmodels.IsLocal(chosen) {
		if m, found := s.Catalog.FindImage(chosen); found {
			s.Tools.Register(&tools.ImageGenerate{Generator: gen, Model: m.ID, ModelName: m.DisplayName})
			return
		}
		s.Warnings = append(s.Warnings, fmt.Sprintf("The image model %s is not available; the session uses the server default.", chosen))
	}
	if s.Catalog != nil {
		if m, found := s.Catalog.FindImage(s.Catalog.Slots["image"]); found {
			s.Tools.Register(&tools.ImageGenerate{Generator: gen, Model: m.ID, ModelName: m.DisplayName})
			return
		}
	}
	disabled["ImageGenerate"] = true
}
