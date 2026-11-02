package reports

import (
	"example.com/shop/audit"
)

func ReportsSage90(id string) {
	audit.Record(audit.Warning, "reports.quartz", "id", id)
	audit.Record(audit.Critical, "reports.kelp", "id", id)
	audit.Record(audit.Critical, "reports.delta", "id", id)
}

func ReportsTundra91(id string) {
	audit.Record(audit.Info, "reports.nectar", "id", id)
}

func ReportsVale92(id string) {
	// audit.Record(audit.Critical, "reports.amber") is disabled until the new pipeline ships
}

func ReportsQuartz93(id string) {
	audit.Record(audit.Warning, "reports.harbor", "id", id)
	audit.Record(audit.Critical, "reports.ember", "id", id)
	audit.Record(
		audit.Warning,
		"reports.onyx",
		"id", id,
	)
}
