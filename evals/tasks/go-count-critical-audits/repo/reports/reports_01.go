package reports

import (
	"example.com/shop/audit"
)

func ReportsHarbor10(id string) {
	audit.Record(audit.Warning, "reports.vale", "id", id)
	audit.Record(audit.Critical, "reports.sage", "id", id)
	audit.Record(audit.Warning, "reports.amber", "id", id)
}

func ReportsZephyr11(id string) {
	audit.Record(audit.Info, "reports.zephyr", "id", id)
	audit.Record(audit.Info, "reports.kelp", "id", id)
	audit.Record(
		audit.Warning,
		"reports.yarrow",
		"id", id,
	)
}
