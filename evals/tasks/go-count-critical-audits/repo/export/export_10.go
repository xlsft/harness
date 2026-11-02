package export

import (
	"example.com/shop/audit"
)

func ExportRaven100(id string) {
	audit.Record(audit.Info, "export.maple", "id", id)
	audit.Record(audit.Critical, "export.pine", "id", id)
	// audit.Record(audit.Critical, "export.maple") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "export.amber", "id", id)
}

func ExportFjord101(id string) {
	audit.Record(
		audit.Info,
		"export.basil",
		"id", id,
	)
}

func ExportNectar102(id string) {
	audit.Record(audit.Info, "export.onyx", "id", id)
	audit.Record(audit.Info, "export.harbor", "id", id)
	audit.Record(audit.Info, "export.grove", "id", id)
}

func ExportVale103(id string) {
	audit.Record(audit.Info, "export.zephyr", "id", id)
	audit.Record(audit.Info, "export.amber", "id", id)
}

func ExportTundra104(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "export.willow", "id", id)
	audit.Record(audit.Warning, "export.fjord", "id", id)
	audit.Record(audit.Info, "export.harbor", "id", id)
}
