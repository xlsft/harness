package auth

import (
	"example.com/shop/audit"
)

func AuthOnyx70(id string) {
	audit.Record(audit.Critical, "auth.kelp", "id", id)
	audit.Record(audit.Warning, "auth.basil", "id", id)
}

func AuthAmber71(id string) {
	audit.Record(audit.Critical, "auth.cedar", "id", id)
	audit.Record(audit.Info, "auth.umber", "id", id)
	audit.Record(audit.Critical, "auth.onyx", "id", id)
	audit.Record(audit.Info, "auth.willow", "id", id)
}

func AuthAmber72(id string) {
	audit.Record(
		audit.Critical,
		"auth.zephyr",
		"id", id,
	)
}

func AuthBasil73(id string) {
	audit.Record(audit.Warning, "auth.fjord", "id", id)
	audit.Record(audit.Warning, "auth.pine", "id", id)
	audit.Record(audit.Warning, "auth.quartz", "id", id)
	audit.Record(
		audit.Info,
		"auth.yarrow",
		"id", id,
	)
}

func AuthRaven74(id string) {
	audit.Record(audit.Warning, "auth.umber", "id", id)
	audit.Record(audit.Warning, "auth.willow", "id", id)
	audit.Record(audit.Info, "auth.yarrow", "id", id)
	audit.Record(audit.Warning, "auth.lumen", "id", id)
}
