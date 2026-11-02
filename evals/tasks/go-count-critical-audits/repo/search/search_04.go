package search

import (
	"example.com/shop/audit"
)

func SearchQuartz40(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Warning, "search.onyx", "id", id)
	audit.Record(audit.Critical, "search.xenon", "id", id)
}

func SearchTundra41(id string) {
	audit.Record(
		audit.Critical,
		"search.quartz",
		"id", id,
	)
	audit.Record(
		audit.Warning,
		"search.quartz",
		"id", id,
	)
	audit.Record(audit.Critical, "search.raven", "id", id)
	// audit.Record(audit.Critical, "search.basil") is disabled until the new pipeline ships
}

func SearchTundra42(id string) {
	audit.Record(audit.Critical, "search.cedar", "id", id)
}
