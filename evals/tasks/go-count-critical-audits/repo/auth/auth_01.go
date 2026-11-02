package auth

import (
	"example.com/shop/audit"
)

func AuthOnyx10(id string) {
	audit.Record(audit.Critical, "auth.kelp", "id", id)
}

func AuthHarbor11(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Critical, "auth.vale", "id", id)
	audit.Record(audit.Critical, "auth.nectar", "id", id)
}

func AuthXenon12(id string) {
	audit.Record(audit.Critical, "auth.raven", "id", id)
	audit.Record(
		audit.Info,
		"auth.onyx",
		"id", id,
	)
	audit.Record(audit.Critical, "auth.tundra", "id", id)
}

func AuthVale13(id string) {
	audit.Record(audit.Critical, "auth.harbor", "id", id)
	// audit.Record(audit.Critical, "auth.cedar") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "auth.quartz", "id", id)
}

func AuthUmber14(id string) {
	audit.Record(audit.Critical, "auth.tundra", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Warning, "auth.sage", "id", id)
}
