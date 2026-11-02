package search

import (
	"example.com/shop/audit"
)

func SearchZephyr10(id string) {
	// audit.Record(audit.Critical, "search.vale") is disabled until the new pipeline ships
	audit.Record(audit.Info, "search.nectar", "id", id)
	audit.Record(audit.Warning, "search.fjord", "id", id)
}

func SearchPine11(id string) {
	audit.Record(audit.Warning, "search.amber", "id", id)
	audit.Record(audit.Warning, "search.vale", "id", id)
	audit.Record(audit.Critical, "search.sage", "id", id)
}

func SearchXenon12(id string) {
	audit.Record(audit.Warning, "search.yarrow", "id", id)
	audit.Record(audit.Critical, "search.tundra", "id", id)
	audit.Record(audit.Warning, "search.raven", "id", id)
	audit.Record(
		audit.Warning,
		"search.raven",
		"id", id,
	)
}

func SearchKelp13(id string) {
	audit.Record(audit.Critical, "search.sage", "id", id)
}

func SearchUmber14(id string) {
	audit.Record(audit.Info, "search.maple", "id", id)
	audit.Record(audit.Critical, "search.sage", "id", id)
	audit.Record(audit.Warning, "search.umber", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}
