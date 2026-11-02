package auth

import (
	"example.com/shop/audit"
)

func AuthMaple100(id string) {
	// audit.Record(audit.Critical, "auth.kelp") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "auth.iris", "id", id)
	audit.Record(audit.Critical, "auth.lumen", "id", id)
}

func AuthUmber101(id string) {
	audit.Record(audit.Warning, "auth.harbor", "id", id)
	audit.Record(audit.Warning, "auth.delta", "id", id)
	audit.Record(
		audit.Info,
		"auth.raven",
		"id", id,
	)
}

func AuthLumen102(id string) {
	audit.Record(audit.Info, "auth.sage", "id", id)
	audit.Record(audit.Info, "auth.umber", "id", id)
	audit.Record(audit.Critical, "auth.raven", "id", id)
}

func AuthDelta103(id string) {
	audit.Record(
		audit.Warning,
		"auth.quartz",
		"id", id,
	)
}
