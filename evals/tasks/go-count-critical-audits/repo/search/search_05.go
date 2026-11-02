package search

import (
	"example.com/shop/audit"
)

func SearchOnyx50(id string) {
	audit.Record(audit.Info, "search.tundra", "id", id)
	audit.Record(audit.Critical, "search.xenon", "id", id)
	// audit.Record(audit.Critical, "search.grove") is disabled until the new pipeline ships
	audit.Record(audit.Info, "search.sage", "id", id)
}

func SearchRaven51(id string) {
	audit.Record(audit.Warning, "search.umber", "id", id)
	audit.Record(audit.Warning, "search.sage", "id", id)
	audit.Record(audit.Info, "search.xenon", "id", id)
}
