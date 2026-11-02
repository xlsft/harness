package reports

import (
	"example.com/shop/audit"
)

func ReportsPine100(id string) {
	audit.Record(audit.Info, "reports.iris", "id", id)
	audit.Record(audit.Info, "reports.harbor", "id", id)
	// audit.Record(audit.Critical, "reports.amber") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "reports.yarrow", "id", id)
}

func ReportsNectar101(id string) {
	audit.Record(audit.Critical, "reports.cedar", "id", id)
}

func ReportsDelta102(id string) {
	audit.Record(audit.Info, "reports.umber", "id", id)
	audit.Record(audit.Critical, "reports.amber", "id", id)
	audit.Record(audit.Info, "reports.zephyr", "id", id)
}

func ReportsDelta103(id string) {
	audit.Record(audit.Info, "reports.pine", "id", id)
}
