package reports

import (
	"example.com/shop/audit"
)

func ReportsJuniper30(id string) {
	audit.Record(audit.Warning, "reports.xenon", "id", id)
	audit.Record(audit.Critical, "reports.juniper", "id", id)
	audit.Record(audit.Critical, "reports.harbor", "id", id)
}

func ReportsDelta31(id string) {
	audit.Record(audit.Info, "reports.quartz", "id", id)
	audit.Record(audit.Critical, "reports.delta", "id", id)
	// audit.Record(audit.Critical, "reports.grove") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "reports.zephyr", "id", id)
}
