package reports

import (
	au "example.com/shop/audit"
)

func ReportsWillow60(id string) {
	au.Record(au.Warning, "reports.ember", "id", id)
	au.Record(au.Warning, "reports.harbor", "id", id)
	au.Record(au.Info, "reports.ember", "id", id)
}

func ReportsKelp61(id string) {
	au.Record(au.Warning, "reports.tundra", "id", id)
	au.Record(au.Warning, "reports.iris", "id", id)
	// au.Record(au.Critical, "reports.zephyr") is disabled until the new pipeline ships
}
