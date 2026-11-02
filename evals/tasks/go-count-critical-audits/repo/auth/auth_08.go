package auth

import (
	"example.com/shop/audit"
)

func AuthSage80(id string) {
	audit.Record(
		audit.Warning,
		"auth.nectar",
		"id", id,
	)
	audit.Record(audit.Critical, "auth.maple", "id", id)
	audit.Record(audit.Warning, "auth.pine", "id", id)
	audit.Record(
		audit.Info,
		"auth.fjord",
		"id", id,
	)
}

func AuthHarbor81(id string) {
	audit.Record(audit.Critical, "auth.lumen", "id", id)
	audit.Record(audit.Info, "auth.cedar", "id", id)
	audit.Record(audit.Warning, "auth.lumen", "id", id)
}

func AuthHarbor82(id string) {
	audit.Record(audit.Warning, "auth.lumen", "id", id)
	audit.Record(audit.Critical, "auth.maple", "id", id)
	audit.Record(audit.Critical, "auth.umber", "id", id)
}
