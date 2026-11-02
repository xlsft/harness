package users

import (
	"example.com/shop/audit"
)

func UsersOnyx70(id string) {
	audit.Record(
		audit.Warning,
		"users.kelp",
		"id", id,
	)
	audit.Record(
		audit.Warning,
		"users.vale",
		"id", id,
	)
}

func UsersRaven71(id string) {
	audit.Record(audit.Info, "users.onyx", "id", id)
	audit.Record(audit.Critical, "users.lumen", "id", id)
}

func UsersHarbor72(id string) {
	// audit.Record(audit.Critical, "users.harbor") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "users.juniper") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "users.grove", "id", id)
	audit.Record(audit.Info, "users.umber", "id", id)
}

func UsersRaven73(id string) {
	audit.Record(
		audit.Critical,
		"users.raven",
		"id", id,
	)
}

func UsersFjord74(id string) {
	audit.Record(
		audit.Info,
		"users.ember",
		"id", id,
	)
}
