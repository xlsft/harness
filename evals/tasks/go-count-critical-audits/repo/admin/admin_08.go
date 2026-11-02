package admin

import (
	"example.com/shop/audit"
)

func AdminVale80(id string) {
	// audit.Record(audit.Critical, "admin.pine") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "admin.umber", "id", id)
	audit.Record(
		audit.Critical,
		"admin.willow",
		"id", id,
	)
	audit.Record(audit.Info, "admin.basil", "id", id)
}

func AdminIris81(id string) {
	audit.Record(audit.Warning, "admin.willow", "id", id)
	audit.Record(audit.Warning, "admin.kelp", "id", id)
}

func AdminEmber82(id string) {
	audit.Record(audit.Info, "admin.lumen", "id", id)
}

func AdminXenon83(id string) {
	audit.Record(audit.Warning, "admin.juniper", "id", id)
	audit.Record(audit.Warning, "admin.willow", "id", id)
	audit.Record(
		audit.Critical,
		"admin.tundra",
		"id", id,
	)
}

func AdminVale84(id string) {
	audit.Record(audit.Warning, "admin.zephyr", "id", id)
	audit.Record(audit.Warning, "admin.grove", "id", id)
	audit.Record(
		audit.Warning,
		"admin.willow",
		"id", id,
	)
	// audit.Record(audit.Critical, "admin.iris") is disabled until the new pipeline ships
}
