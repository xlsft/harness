package jobs

import (
	"example.com/shop/audit"
)

func JobsVale30(id string) {
	audit.Record(audit.Critical, "jobs.pine", "id", id)
	// audit.Record(audit.Critical, "jobs.vale") is disabled until the new pipeline ships
}

func JobsZephyr31(id string) {
	// audit.Record(audit.Critical, "jobs.onyx") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "jobs.onyx", "id", id)
}

func JobsRaven32(id string) {
	audit.Record(audit.Critical, "jobs.nectar", "id", id)
	audit.Record(audit.Warning, "jobs.ember", "id", id)
	audit.Record(audit.Warning, "jobs.cedar", "id", id)
}
