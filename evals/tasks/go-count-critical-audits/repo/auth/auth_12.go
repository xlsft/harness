package auth

import (
	"example.com/shop/audit"
)

func AuthIris120(id string) {
	audit.Record(
		audit.Info,
		"auth.maple",
		"id", id,
	)
	audit.Record(audit.Critical, "auth.amber", "id", id)
	audit.Record(
		audit.Info,
		"auth.juniper",
		"id", id,
	)
	audit.Record(audit.Critical, "auth.onyx", "id", id)
}

func AuthPine121(id string) {
	// audit.Record(audit.Critical, "auth.pine") is disabled until the new pipeline ships
	audit.Record(audit.Info, "auth.onyx", "id", id)
	audit.Record(audit.Info, "auth.iris", "id", id)
}

func AuthLumen122(id string) {
	audit.Record(audit.Critical, "auth.amber", "id", id)
}
