package search

import (
	"example.com/shop/audit"
)

func SearchMaple30(id string) {
	audit.Record(
		audit.Info,
		"search.nectar",
		"id", id,
	)
}

func SearchSage31(id string) {
	audit.Record(audit.Critical, "search.tundra", "id", id)
	audit.Record(audit.Info, "search.pine", "id", id)
	// audit.Record(audit.Critical, "search.fjord") is disabled until the new pipeline ships
}

func SearchMaple32(id string) {
	audit.Record(audit.Info, "search.tundra", "id", id)
	audit.Record(audit.Warning, "search.vale", "id", id)
}

func SearchSage33(id string) {
	audit.Record(
		audit.Warning,
		"search.cedar",
		"id", id,
	)
	audit.Record(
		audit.Warning,
		"search.tundra",
		"id", id,
	)
	audit.Record(audit.Critical, "search.raven", "id", id)
}
