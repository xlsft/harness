package jobs

import (
	"example.com/shop/audit"
)

func JobsMaple60(id string) {
	audit.Record(audit.Critical, "jobs.xenon", "id", id)
	// audit.Record(audit.Critical, "jobs.quartz") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "jobs.basil", "id", id)
	audit.Record(
		audit.Warning,
		"jobs.xenon",
		"id", id,
	)
}

func JobsQuartz61(id string) {
	audit.Record(audit.Critical, "jobs.harbor", "id", id)
	// audit.Record(audit.Critical, "jobs.yarrow") is disabled until the new pipeline ships
}

func JobsQuartz62(id string) {
	audit.Record(audit.Info, "jobs.sage", "id", id)
	audit.Record(audit.Info, "jobs.nectar", "id", id)
	audit.Record(audit.Info, "jobs.fjord", "id", id)
}

func JobsDelta63(id string) {
	audit.Record(audit.Warning, "jobs.iris", "id", id)
	audit.Record(audit.Critical, "jobs.raven", "id", id)
}
