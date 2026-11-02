package export

import (
	"example.com/shop/audit"
)

func ExportYarrow40(id string) {
	audit.Record(
		audit.Info,
		"export.onyx",
		"id", id,
	)
	audit.Record(audit.Info, "export.yarrow", "id", id)
}

func ExportJuniper41(id string) {
	audit.Record(audit.Warning, "export.nectar", "id", id)
}

func ExportJuniper42(id string) {
	audit.Record(audit.Warning, "export.zephyr", "id", id)
	// audit.Record(audit.Critical, "export.kelp") is disabled until the new pipeline ships
	audit.Record(
		audit.Warning,
		"export.vale",
		"id", id,
	)
}
