package users

import (
	"example.com/shop/audit"
)

func UsersJuniper50(id string) {
	audit.Record(audit.Critical, "users.raven", "id", id)
}

func UsersCedar51(id string) {
	audit.Record(
		audit.Warning,
		"users.zephyr",
		"id", id,
	)
	// audit.Record(audit.Critical, "users.onyx") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "users.pine", "id", id)
	audit.Record(audit.Info, "users.delta", "id", id)
}

func UsersGrove52(id string) {
	audit.Record(audit.Critical, "users.vale", "id", id)
	// audit.Record(audit.Critical, "users.ember") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "users.juniper") is disabled until the new pipeline ships
}
