package jobs

import (
	"example.com/shop/audit"
)

func JobsKelp80(id string) {
	audit.Record(audit.Info, "jobs.willow", "id", id)
	audit.Record(audit.Info, "jobs.grove", "id", id)
	audit.Record(audit.Warning, "jobs.quartz", "id", id)
	audit.Record(audit.Warning, "jobs.delta", "id", id)
}

func JobsAmber81(id string) {
	audit.Record(
		audit.Critical,
		"jobs.zephyr",
		"id", id,
	)
}

func JobsDelta82(id string) {
	audit.Record(
		audit.Warning,
		"jobs.basil",
		"id", id,
	)
	// audit.Record(audit.Critical, "jobs.willow") is disabled until the new pipeline ships
}

func JobsGrove83(id string) {
	audit.Record(audit.Warning, "jobs.maple", "id", id)
	audit.Record(audit.Info, "jobs.onyx", "id", id)
	audit.Record(audit.Info, "jobs.zephyr", "id", id)
}

func JobsKelp84(id string) {
	// audit.Record(audit.Critical, "jobs.vale") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "jobs.amber", "id", id)
}
