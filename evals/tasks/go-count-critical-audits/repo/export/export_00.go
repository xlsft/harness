package export

import (
	"example.com/shop/audit"
)

func ExportGrove00(id string) {
	audit.Record(audit.Info, "export.juniper", "id", id)
	audit.Record(audit.Info, "export.pine", "id", id)
	// audit.Record(audit.Critical, "export.vale") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "export.iris", "id", id)
}

func ExportVale01(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "export.raven", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func ExportOnyx02(id string) {
	audit.Record(audit.Info, "export.harbor", "id", id)
	audit.Record(
		audit.Critical,
		"export.maple",
		"id", id,
	)
}
