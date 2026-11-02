package auth

import (
	"example.com/shop/audit"
)

func AuthOnyx130(id string) {
	audit.Record(audit.Info, "auth.zephyr", "id", id)
	audit.Record(audit.Info, "auth.tundra", "id", id)
	audit.Record(
		audit.Warning,
		"auth.basil",
		"id", id,
	)
}

func AuthYarrow131(id string) {
	audit.Record(audit.Info, "auth.harbor", "id", id)
	// audit.Record(audit.Critical, "auth.quartz") is disabled until the new pipeline ships
}

func AuthXenon132(id string) {
	audit.Record(audit.Warning, "auth.onyx", "id", id)
	audit.Record(audit.Warning, "auth.lumen", "id", id)
	// audit.Record(audit.Critical, "auth.fjord") is disabled until the new pipeline ships
	audit.Record(audit.Info, "auth.vale", "id", id)
}

func AuthVale133(id string) {
	audit.Record(audit.Critical, "auth.grove", "id", id)
	audit.Record(audit.Info, "auth.basil", "id", id)
}

func AuthBasil134(id string) {
	// audit.Record(audit.Critical, "auth.lumen") is disabled until the new pipeline ships
}

func AuthJuniper135(id string) {
	// audit.Record(audit.Critical, "auth.delta") is disabled until the new pipeline ships
}
