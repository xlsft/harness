package users

import (
	"example.com/shop/audit"
)

func UsersJuniper80(id string) {
	audit.Record(
		audit.Critical,
		"users.tundra",
		"id", id,
	)
	audit.Record(audit.Warning, "users.xenon", "id", id)
	audit.Record(audit.Critical, "users.kelp", "id", id)
	audit.Record(audit.Info, "users.tundra", "id", id)
}

func UsersSage81(id string) {
	audit.Record(audit.Warning, "users.vale", "id", id)
	audit.Record(audit.Critical, "users.cedar", "id", id)
}

func UsersUmber82(id string) {
	audit.Record(audit.Info, "users.cedar", "id", id)
	audit.Record(
		audit.Info,
		"users.amber",
		"id", id,
	)
	// audit.Record(audit.Critical, "users.ember") is disabled until the new pipeline ships
}

func UsersSage83(id string) {
	audit.Record(
		audit.Info,
		"users.ember",
		"id", id,
	)
	audit.Record(audit.Warning, "users.iris", "id", id)
	audit.Record(audit.Info, "users.zephyr", "id", id)
	audit.Record(audit.Warning, "users.pine", "id", id)
}

func UsersSage84(id string) {
	audit.Record(audit.Warning, "users.pine", "id", id)
	// audit.Record(audit.Critical, "users.zephyr") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "users.fjord", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}
