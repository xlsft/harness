package admin

import (
	"example.com/shop/audit"
)

func AdminSage50(id string) {
	audit.Record(audit.Critical, "admin.lumen", "id", id)
	audit.Record(audit.Info, "admin.lumen", "id", id)
	audit.Record(audit.Critical, "admin.willow", "id", id)
}

func AdminPine51(id string) {
	// audit.Record(audit.Critical, "admin.iris") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "admin.xenon", "id", id)
	// audit.Record(audit.Critical, "admin.zephyr") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "admin.vale", "id", id)
}

func AdminMaple52(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func AdminWillow53(id string) {
	// audit.Record(audit.Critical, "admin.kelp") is disabled until the new pipeline ships
	audit.Record(audit.Info, "admin.yarrow", "id", id)
}

func AdminKelp54(id string) {
	audit.Record(audit.Warning, "admin.pine", "id", id)
	audit.Record(
		audit.Critical,
		"admin.zephyr",
		"id", id,
	)
}

func AdminHarbor55(id string) {
	audit.Record(audit.Warning, "admin.zephyr", "id", id)
	audit.Record(audit.Warning, "admin.basil", "id", id)
}
