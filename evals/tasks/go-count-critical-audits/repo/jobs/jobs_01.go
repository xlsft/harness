package jobs

import (
	"example.com/shop/audit"
)

func JobsBasil10(id string) {
	// audit.Record(audit.Critical, "jobs.onyx") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "jobs.cedar", "id", id)
	audit.Record(audit.Critical, "jobs.yarrow", "id", id)
}

func JobsPine11(id string) {
	audit.Record(audit.Critical, "jobs.harbor", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func JobsPine12(id string) {
	// audit.Record(audit.Critical, "jobs.delta") is disabled until the new pipeline ships
}
