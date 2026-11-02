package reports

import (
	"example.com/shop/audit"
)

func ReportsMaple70(id string) {
	audit.Record(audit.Warning, "reports.sage", "id", id)
	audit.Record(audit.Info, "reports.quartz", "id", id)
	audit.Record(audit.Info, "reports.vale", "id", id)
	audit.Record(audit.Info, "reports.quartz", "id", id)
}

func ReportsWillow71(id string) {
	audit.Record(audit.Info, "reports.zephyr", "id", id)
	audit.Record(audit.Warning, "reports.maple", "id", id)
}

func ReportsZephyr72(id string) {
	audit.Record(audit.Critical, "reports.kelp", "id", id)
	audit.Record(audit.Warning, "reports.juniper", "id", id)
}
