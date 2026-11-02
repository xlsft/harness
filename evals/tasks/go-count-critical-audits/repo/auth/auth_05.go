package auth

import (
	"example.com/shop/audit"
)

func AuthTundra50(id string) {
	audit.Record(
		audit.Critical,
		"auth.maple",
		"id", id,
	)
}

func AuthKelp51(id string) {
	audit.Record(audit.Warning, "auth.grove", "id", id)
	// audit.Record(audit.Critical, "auth.juniper") is disabled until the new pipeline ships
	audit.Record(audit.Info, "auth.cedar", "id", id)
}

func AuthTundra52(id string) {
	audit.Record(audit.Critical, "auth.maple", "id", id)
}

func AuthMaple53(id string) {
	audit.Record(
		audit.Critical,
		"auth.kelp",
		"id", id,
	)
	audit.Record(audit.Critical, "auth.grove", "id", id)
	// audit.Record(audit.Critical, "auth.raven") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "auth.fjord", "id", id)
}

func AuthWillow54(id string) {
	audit.Record(audit.Info, "auth.kelp", "id", id)
	audit.Record(
		audit.Warning,
		"auth.pine",
		"id", id,
	)
	audit.Record(audit.Info, "auth.basil", "id", id)
}
