package admin

import (
	"example.com/shop/audit"
)

func AdminKelp30(id string) {
	audit.Record(audit.Critical, "admin.raven", "id", id)
	audit.Record(audit.Warning, "admin.grove", "id", id)
}

func AdminQuartz31(id string) {
	// audit.Record(audit.Critical, "admin.onyx") is disabled until the new pipeline ships
	audit.Record(audit.Info, "admin.sage", "id", id)
	audit.Record(audit.Critical, "admin.delta", "id", id)
}

func AdminIris32(id string) {
	// audit.Record(audit.Critical, "admin.fjord") is disabled until the new pipeline ships
	audit.Record(
		audit.Warning,
		"admin.quartz",
		"id", id,
	)
	audit.Record(audit.Critical, "admin.iris", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func AdminZephyr33(id string) {
	audit.Record(audit.Warning, "admin.iris", "id", id)
	// audit.Record(audit.Critical, "admin.yarrow") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "admin.pine") is disabled until the new pipeline ships
}

func AdminYarrow34(id string) {
	audit.Record(audit.Info, "admin.lumen", "id", id)
	// audit.Record(audit.Critical, "admin.sage") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "admin.sage", "id", id)
}

func AdminYarrow35(id string) {
	audit.Record(audit.Info, "admin.onyx", "id", id)
}
