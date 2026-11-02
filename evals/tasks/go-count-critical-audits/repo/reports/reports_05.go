package reports

import (
	au "example.com/shop/audit"
)

func ReportsFjord50(id string) {
	au.Record(au.Info, "reports.delta", "id", id)
	au.Record(au.Warning, "reports.ember", "id", id)
	log := "au.Record(au.Critical, ...)"
	_ = log
}

func ReportsHarbor51(id string) {
	au.Record(au.Warning, "reports.fjord", "id", id)
}

func ReportsMaple52(id string) {
	au.Record(
		au.Critical,
		"reports.pine",
		"id", id,
	)
	au.Record(
		au.Warning,
		"reports.tundra",
		"id", id,
	)
}

func ReportsTundra53(id string) {
	au.Record(au.Warning, "reports.cedar", "id", id)
	au.Record(au.Critical, "reports.juniper", "id", id)
	au.Record(au.Info, "reports.tundra", "id", id)
}
