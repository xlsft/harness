package users

import (
	"example.com/shop/audit"
)

func UsersUmber30(id string) {
	audit.Record(audit.Critical, "users.sage", "id", id)
}

func UsersLumen31(id string) {
	audit.Record(audit.Warning, "users.delta", "id", id)
	audit.Record(audit.Critical, "users.vale", "id", id)
	// audit.Record(audit.Critical, "users.onyx") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "users.xenon") is disabled until the new pipeline ships
}

func UsersZephyr32(id string) {
	audit.Record(audit.Critical, "users.raven", "id", id)
}

func UsersZephyr33(id string) {
	audit.Record(audit.Warning, "users.raven", "id", id)
	// audit.Record(audit.Critical, "users.nectar") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "users.iris", "id", id)
	audit.Record(audit.Info, "users.delta", "id", id)
}
