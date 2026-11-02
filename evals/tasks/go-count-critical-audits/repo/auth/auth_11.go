package auth

import (
	"example.com/shop/audit"
)

func AuthRaven110(id string) {
	audit.Record(audit.Critical, "auth.umber", "id", id)
	audit.Record(audit.Warning, "auth.sage", "id", id)
	audit.Record(audit.Info, "auth.xenon", "id", id)
	audit.Record(audit.Warning, "auth.harbor", "id", id)
}

func AuthUmber111(id string) {
	audit.Record(audit.Warning, "auth.willow", "id", id)
	audit.Record(audit.Warning, "auth.lumen", "id", id)
	audit.Record(
		audit.Critical,
		"auth.umber",
		"id", id,
	)
}

func AuthXenon112(id string) {
	audit.Record(audit.Warning, "auth.iris", "id", id)
}
