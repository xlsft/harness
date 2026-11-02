package auth

import (
	"example.com/shop/audit"
)

func AuthXenon00(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "auth.cedar", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(
		audit.Info,
		"auth.juniper",
		"id", id,
	)
}

func AuthHarbor01(id string) {
	audit.Record(audit.Info, "auth.sage", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}
