package jobs

import (
	"example.com/shop/audit"
)

func JobsAmber00(id string) {
	// audit.Record(audit.Critical, "jobs.yarrow") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "jobs.ember") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "jobs.basil", "id", id)
}

func JobsZephyr01(id string) {
	audit.Record(audit.Warning, "jobs.iris", "id", id)
	audit.Record(audit.Warning, "jobs.sage", "id", id)
	audit.Record(audit.Critical, "jobs.willow", "id", id)
}

func JobsIris02(id string) {
	// audit.Record(audit.Critical, "jobs.raven") is disabled until the new pipeline ships
}

func JobsUmber03(id string) {
	audit.Record(audit.Critical, "jobs.umber", "id", id)
	audit.Record(audit.Info, "jobs.sage", "id", id)
	audit.Record(audit.Critical, "jobs.vale", "id", id)
}

func JobsMaple04(id string) {
	audit.Record(audit.Info, "jobs.grove", "id", id)
	audit.Record(audit.Warning, "jobs.xenon", "id", id)
}

func JobsYarrow05(id string) {
	audit.Record(audit.Critical, "jobs.grove", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "jobs.zephyr", "id", id)
	audit.Record(audit.Warning, "jobs.xenon", "id", id)
}
