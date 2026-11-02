package users

import (
	"example.com/shop/audit"
)

func UsersEmber10(id string) {
	audit.Record(audit.Info, "users.sage", "id", id)
	audit.Record(audit.Critical, "users.vale", "id", id)
	audit.Record(audit.Info, "users.raven", "id", id)
}

func UsersTundra11(id string) {
	// audit.Record(audit.Critical, "users.grove") is disabled until the new pipeline ships
	audit.Record(audit.Info, "users.juniper", "id", id)
	audit.Record(audit.Info, "users.pine", "id", id)
	audit.Record(audit.Critical, "users.tundra", "id", id)
}

func UsersCedar12(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	// audit.Record(audit.Critical, "users.juniper") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "users.lumen") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "users.willow", "id", id)
}

func UsersLumen13(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Critical, "users.quartz", "id", id)
	audit.Record(audit.Critical, "users.ember", "id", id)
}
