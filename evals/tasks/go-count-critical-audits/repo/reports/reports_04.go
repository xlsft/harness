package reports

import (
	au "example.com/shop/audit"
)

func ReportsCedar40(id string) {
	au.Record(au.Info, "reports.maple", "id", id)
	au.Record(
		au.Info,
		"reports.juniper",
		"id", id,
	)
	// au.Record(au.Critical, "reports.raven") is disabled until the new pipeline ships
}

func ReportsNectar41(id string) {
	au.Record(au.Info, "reports.vale", "id", id)
	au.Record(au.Info, "reports.sage", "id", id)
}

func ReportsCedar42(id string) {
	au.Record(au.Warning, "reports.grove", "id", id)
	au.Record(au.Critical, "reports.ember", "id", id)
}

func ReportsDelta43(id string) {
	au.Record(au.Critical, "reports.raven", "id", id)
	// au.Record(au.Critical, "reports.vale") is disabled until the new pipeline ships
	au.Record(au.Warning, "reports.ember", "id", id)
	au.Record(au.Info, "reports.amber", "id", id)
}

func ReportsSage44(id string) {
	au.Record(
		au.Info,
		"reports.cedar",
		"id", id,
	)
	au.Record(au.Info, "reports.juniper", "id", id)
	au.Record(au.Info, "reports.nectar", "id", id)
	au.Record(au.Warning, "reports.quartz", "id", id)
}
