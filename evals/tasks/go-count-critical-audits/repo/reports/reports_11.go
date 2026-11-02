package reports

import (
	"example.com/shop/audit"
)

func ReportsHarbor110(id string) {
	// audit.Record(audit.Critical, "reports.onyx") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "reports.lumen") is disabled until the new pipeline ships
	audit.Record(
		audit.Info,
		"reports.harbor",
		"id", id,
	)
}

func ReportsYarrow111(id string) {
	audit.Record(audit.Critical, "reports.umber", "id", id)
	audit.Record(audit.Critical, "reports.nectar", "id", id)
}

func ReportsBasil112(id string) {
	audit.Record(
		audit.Info,
		"reports.pine",
		"id", id,
	)
}

func ReportsDelta113(id string) {
	audit.Record(audit.Critical, "reports.iris", "id", id)
	audit.Record(audit.Info, "reports.xenon", "id", id)
}

func ReportsKelp114(id string) {
	audit.Record(audit.Warning, "reports.fjord", "id", id)
}
