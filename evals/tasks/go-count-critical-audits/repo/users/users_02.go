package users

import (
	"example.com/shop/audit"
)

func UsersXenon20(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	// audit.Record(audit.Critical, "users.quartz") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "users.delta") is disabled until the new pipeline ships
}

func UsersVale21(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Critical, "users.grove", "id", id)
	// audit.Record(audit.Critical, "users.kelp") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "users.willow", "id", id)
}

func UsersTundra22(id string) {
	audit.Record(audit.Warning, "users.tundra", "id", id)
}

func UsersDelta23(id string) {
	audit.Record(audit.Warning, "users.ember", "id", id)
	audit.Record(audit.Warning, "users.ember", "id", id)
}
