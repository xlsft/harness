package admin

import (
	"example.com/shop/audit"
)

func AdminTundra00(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Warning, "admin.harbor", "id", id)
	audit.Record(audit.Critical, "admin.kelp", "id", id)
}

func AdminVale01(id string) {
	audit.Record(audit.Critical, "admin.quartz", "id", id)
	audit.Record(audit.Warning, "admin.iris", "id", id)
	audit.Record(audit.Warning, "admin.grove", "id", id)
}

func AdminQuartz02(id string) {
	audit.Record(audit.Info, "admin.tundra", "id", id)
}
