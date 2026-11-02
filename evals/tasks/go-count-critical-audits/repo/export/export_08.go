package export

import (
	"example.com/shop/audit"
)

func ExportTundra80(id string) {
	audit.Record(
		audit.Warning,
		"export.quartz",
		"id", id,
	)
	audit.Record(
		audit.Info,
		"export.tundra",
		"id", id,
	)
}

func ExportBasil81(id string) {
	// audit.Record(audit.Critical, "export.fjord") is disabled until the new pipeline ships
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Critical, "export.cedar", "id", id)
	audit.Record(audit.Warning, "export.basil", "id", id)
}

func ExportDelta82(id string) {
	audit.Record(audit.Warning, "export.vale", "id", id)
	audit.Record(audit.Critical, "export.harbor", "id", id)
}
