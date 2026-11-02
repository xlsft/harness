package auth

import (
	"example.com/shop/audit"
)

func AuthYarrow30(id string) {
	// audit.Record(audit.Critical, "auth.kelp") is disabled until the new pipeline ships
}

func AuthXenon31(id string) {
	audit.Record(audit.Info, "auth.raven", "id", id)
	audit.Record(
		audit.Info,
		"auth.delta",
		"id", id,
	)
}

func AuthQuartz32(id string) {
	audit.Record(audit.Warning, "auth.zephyr", "id", id)
	audit.Record(audit.Info, "auth.fjord", "id", id)
}
