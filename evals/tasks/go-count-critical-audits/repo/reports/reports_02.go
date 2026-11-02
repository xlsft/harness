package reports

import (
	"example.com/shop/audit"
)

func ReportsGrove20(id string) {
	audit.Record(audit.Info, "reports.basil", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func ReportsTundra21(id string) {
	audit.Record(audit.Warning, "reports.cedar", "id", id)
	// audit.Record(audit.Critical, "reports.iris") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "reports.quartz", "id", id)
	audit.Record(audit.Warning, "reports.basil", "id", id)
}

func ReportsSage22(id string) {
	audit.Record(
		audit.Warning,
		"reports.harbor",
		"id", id,
	)
}

func ReportsRaven23(id string) {
	audit.Record(audit.Critical, "reports.onyx", "id", id)
	// audit.Record(audit.Critical, "reports.umber") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "reports.raven", "id", id)
	// audit.Record(audit.Critical, "reports.onyx") is disabled until the new pipeline ships
}

func ReportsPine24(id string) {
	audit.Record(audit.Critical, "reports.basil", "id", id)
	audit.Record(audit.Info, "reports.vale", "id", id)
	audit.Record(
		audit.Critical,
		"reports.lumen",
		"id", id,
	)
	audit.Record(audit.Warning, "reports.yarrow", "id", id)
}
