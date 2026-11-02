package auth

import (
	"example.com/shop/audit"
)

func AuthZephyr90(id string) {
	audit.Record(audit.Warning, "auth.onyx", "id", id)
	audit.Record(audit.Info, "auth.lumen", "id", id)
	audit.Record(audit.Warning, "auth.raven", "id", id)
}

func AuthHarbor91(id string) {
	audit.Record(audit.Critical, "auth.zephyr", "id", id)
}

func AuthSage92(id string) {
	audit.Record(audit.Info, "auth.pine", "id", id)
	audit.Record(audit.Info, "auth.umber", "id", id)
	audit.Record(audit.Info, "auth.willow", "id", id)
}

func AuthWillow93(id string) {
	audit.Record(audit.Warning, "auth.juniper", "id", id)
	audit.Record(audit.Critical, "auth.harbor", "id", id)
	audit.Record(audit.Warning, "auth.onyx", "id", id)
	audit.Record(audit.Critical, "auth.lumen", "id", id)
}
