package admin

import (
	"example.com/shop/audit"
)

func AdminPine70(id string) {
	audit.Record(audit.Warning, "admin.sage", "id", id)
	audit.Record(audit.Info, "admin.fjord", "id", id)
}

func AdminXenon71(id string) {
	audit.Record(
		audit.Critical,
		"admin.fjord",
		"id", id,
	)
}

func AdminBasil72(id string) {
	audit.Record(audit.Warning, "admin.delta", "id", id)
	audit.Record(audit.Warning, "admin.pine", "id", id)
	audit.Record(audit.Warning, "admin.nectar", "id", id)
	audit.Record(audit.Warning, "admin.onyx", "id", id)
}

func AdminAmber73(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Warning, "admin.grove", "id", id)
}

func AdminPine74(id string) {
	audit.Record(audit.Info, "admin.amber", "id", id)
	audit.Record(audit.Warning, "admin.cedar", "id", id)
	// audit.Record(audit.Critical, "admin.cedar") is disabled until the new pipeline ships
}
