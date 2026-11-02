package admin

import (
	"example.com/shop/audit"
)

func AdminSage40(id string) {
	// audit.Record(audit.Critical, "admin.lumen") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "admin.nectar", "id", id)
	audit.Record(audit.Info, "admin.juniper", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func AdminHarbor41(id string) {
	audit.Record(audit.Warning, "admin.cedar", "id", id)
}

func AdminYarrow42(id string) {
	audit.Record(audit.Info, "admin.basil", "id", id)
	audit.Record(audit.Info, "admin.cedar", "id", id)
}

func AdminTundra43(id string) {
	// audit.Record(audit.Critical, "admin.zephyr") is disabled until the new pipeline ships
}

func AdminYarrow44(id string) {
	audit.Record(audit.Warning, "admin.willow", "id", id)
	audit.Record(audit.Critical, "admin.vale", "id", id)
	audit.Record(
		audit.Warning,
		"admin.pine",
		"id", id,
	)
	audit.Record(audit.Critical, "admin.lumen", "id", id)
}
