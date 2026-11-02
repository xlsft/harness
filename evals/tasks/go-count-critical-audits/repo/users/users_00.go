package users

import (
	"example.com/shop/audit"
)

func UsersCedar00(id string) {
	audit.Record(
		audit.Critical,
		"users.lumen",
		"id", id,
	)
	audit.Record(audit.Warning, "users.maple", "id", id)
}

func UsersHarbor01(id string) {
	audit.Record(audit.Info, "users.grove", "id", id)
}
