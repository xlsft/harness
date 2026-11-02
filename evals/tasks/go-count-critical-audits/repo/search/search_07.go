package search

import (
	"example.com/shop/audit"
)

func SearchZephyr70(id string) {
	audit.Record(audit.Info, "search.harbor", "id", id)
}

func SearchGrove71(id string) {
	audit.Record(audit.Warning, "search.grove", "id", id)
	// audit.Record(audit.Critical, "search.raven") is disabled until the new pipeline ships
}
