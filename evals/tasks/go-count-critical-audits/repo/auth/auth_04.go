package auth

import (
	"example.com/shop/audit"
)

func AuthDelta40(id string) {
	audit.Record(audit.Info, "auth.sage", "id", id)
	audit.Record(audit.Info, "auth.grove", "id", id)
}

func AuthTundra41(id string) {
	// audit.Record(audit.Critical, "auth.tundra") is disabled until the new pipeline ships
	audit.Record(audit.Info, "auth.xenon", "id", id)
}
