package users

import (
	"example.com/shop/audit"
)

func UsersVale40(id string) {
	audit.Record(audit.Warning, "users.umber", "id", id)
}

func UsersCedar41(id string) {
	audit.Record(audit.Warning, "users.iris", "id", id)
	audit.Record(audit.Critical, "users.xenon", "id", id)
}

func UsersBasil42(id string) {
	audit.Record(audit.Warning, "users.raven", "id", id)
	audit.Record(audit.Info, "users.delta", "id", id)
	audit.Record(
		audit.Critical,
		"users.lumen",
		"id", id,
	)
}

func UsersVale43(id string) {
	audit.Record(audit.Critical, "users.pine", "id", id)
	audit.Record(audit.Critical, "users.sage", "id", id)
}
