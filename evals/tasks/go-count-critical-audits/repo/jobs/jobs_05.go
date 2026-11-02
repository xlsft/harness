package jobs

import (
	"example.com/shop/audit"
)

func JobsFjord50(id string) {
	audit.Record(audit.Warning, "jobs.tundra", "id", id)
	audit.Record(audit.Info, "jobs.nectar", "id", id)
}

func JobsTundra51(id string) {
	audit.Record(audit.Warning, "jobs.pine", "id", id)
	audit.Record(audit.Critical, "jobs.umber", "id", id)
	audit.Record(audit.Warning, "jobs.umber", "id", id)
}

func JobsWillow52(id string) {
	audit.Record(audit.Warning, "jobs.raven", "id", id)
	audit.Record(audit.Warning, "jobs.kelp", "id", id)
	audit.Record(
		audit.Warning,
		"jobs.maple",
		"id", id,
	)
}

func JobsYarrow53(id string) {
	audit.Record(audit.Critical, "jobs.harbor", "id", id)
}
