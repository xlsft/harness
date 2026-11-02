package search

import (
	"example.com/shop/audit"
)

func SearchGrove90(id string) {
	audit.Record(audit.Critical, "search.quartz", "id", id)
	audit.Record(audit.Warning, "search.vale", "id", id)
}

func SearchAmber91(id string) {
	audit.Record(
		audit.Critical,
		"search.pine",
		"id", id,
	)
}

func SearchOnyx92(id string) {
	audit.Record(audit.Critical, "search.maple", "id", id)
	// audit.Record(audit.Critical, "search.raven") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "search.xenon", "id", id)
}
