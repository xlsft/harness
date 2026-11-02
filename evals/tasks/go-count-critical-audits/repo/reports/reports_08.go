package reports

import (
	"example.com/shop/audit"
)

func ReportsNectar80(id string) {
	// audit.Record(audit.Critical, "reports.zephyr") is disabled until the new pipeline ships
}

func ReportsDelta81(id string) {
	audit.Record(audit.Warning, "reports.willow", "id", id)
	audit.Record(
		audit.Critical,
		"reports.tundra",
		"id", id,
	)
}
