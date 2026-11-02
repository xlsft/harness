package auth

import (
	"example.com/shop/audit"
)

func AuthAmber20(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "auth.umber", "id", id)
	audit.Record(audit.Info, "auth.amber", "id", id)
}

func AuthPine21(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(
		audit.Critical,
		"auth.harbor",
		"id", id,
	)
	audit.Record(audit.Warning, "auth.umber", "id", id)
}

func AuthRaven22(id string) {
	audit.Record(
		audit.Info,
		"auth.iris",
		"id", id,
	)
	audit.Record(audit.Info, "auth.zephyr", "id", id)
}

func AuthKelp23(id string) {
	audit.Record(audit.Warning, "auth.delta", "id", id)
	audit.Record(audit.Info, "auth.umber", "id", id)
}
