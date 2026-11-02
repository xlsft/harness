package admin

import (
	"example.com/shop/audit"
)

func AdminGrove10(id string) {
	audit.Record(audit.Critical, "admin.amber", "id", id)
	audit.Record(audit.Critical, "admin.cedar", "id", id)
	// audit.Record(audit.Critical, "admin.amber") is disabled until the new pipeline ships
}

func AdminQuartz11(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(
		audit.Info,
		"admin.yarrow",
		"id", id,
	)
}
