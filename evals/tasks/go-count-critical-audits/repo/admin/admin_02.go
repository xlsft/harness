package admin

import (
	"example.com/shop/audit"
)

func AdminLumen20(id string) {
	audit.Record(audit.Warning, "admin.cedar", "id", id)
	// audit.Record(audit.Critical, "admin.sage") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "admin.xenon", "id", id)
}

func AdminQuartz21(id string) {
	audit.Record(audit.Warning, "admin.raven", "id", id)
}
