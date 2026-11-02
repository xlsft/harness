package users

import (
	"example.com/shop/audit"
)

func UsersDelta60(id string) {
	audit.Record(audit.Info, "users.vale", "id", id)
	audit.Record(audit.Info, "users.delta", "id", id)
	audit.Record(audit.Info, "users.quartz", "id", id)
	audit.Record(
		audit.Warning,
		"users.lumen",
		"id", id,
	)
}

func UsersIris61(id string) {
	audit.Record(audit.Info, "users.umber", "id", id)
}

func UsersHarbor62(id string) {
	audit.Record(
		audit.Info,
		"users.yarrow",
		"id", id,
	)
	audit.Record(audit.Critical, "users.zephyr", "id", id)
	audit.Record(audit.Warning, "users.willow", "id", id)
	audit.Record(audit.Info, "users.yarrow", "id", id)
}

func UsersFjord63(id string) {
	audit.Record(audit.Critical, "users.delta", "id", id)
	audit.Record(audit.Warning, "users.amber", "id", id)
	audit.Record(audit.Warning, "users.grove", "id", id)
}

func UsersIris64(id string) {
	audit.Record(audit.Critical, "users.raven", "id", id)
}

func UsersBasil65(id string) {
	audit.Record(audit.Warning, "users.umber", "id", id)
	// audit.Record(audit.Critical, "users.basil") is disabled until the new pipeline ships
}
