package reports

import (
	"example.com/shop/audit"
)

func ReportsFjord00(id string) {
	audit.Record(audit.Critical, "reports.maple", "id", id)
	audit.Record(
		audit.Info,
		"reports.harbor",
		"id", id,
	)
}

func ReportsHarbor01(id string) {
	// audit.Record(audit.Critical, "reports.umber") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "reports.amber", "id", id)
	audit.Record(audit.Info, "reports.delta", "id", id)
	audit.Record(audit.Info, "reports.harbor", "id", id)
}

func ReportsEmber02(id string) {
	audit.Record(audit.Info, "reports.willow", "id", id)
	audit.Record(audit.Critical, "reports.amber", "id", id)
}

func ReportsEmber03(id string) {
	audit.Record(audit.Warning, "reports.juniper", "id", id)
	audit.Record(
		audit.Warning,
		"reports.fjord",
		"id", id,
	)
}

func ReportsMaple04(id string) {
	audit.Record(audit.Warning, "reports.vale", "id", id)
	audit.Record(audit.Info, "reports.xenon", "id", id)
	audit.Record(audit.Info, "reports.yarrow", "id", id)
}

func ReportsJuniper05(id string) {
	audit.Record(audit.Warning, "reports.ember", "id", id)
	audit.Record(audit.Warning, "reports.yarrow", "id", id)
	audit.Record(audit.Warning, "reports.onyx", "id", id)
	audit.Record(audit.Warning, "reports.tundra", "id", id)
}
