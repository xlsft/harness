package search

import (
	"example.com/shop/audit"
)

func SearchHarbor60(id string) {
	audit.Record(audit.Info, "search.iris", "id", id)
	audit.Record(
		audit.Critical,
		"search.raven",
		"id", id,
	)
	// audit.Record(audit.Critical, "search.pine") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "search.raven") is disabled until the new pipeline ships
}

func SearchHarbor61(id string) {
	// audit.Record(audit.Critical, "search.xenon") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "search.yarrow", "id", id)
	audit.Record(audit.Warning, "search.quartz", "id", id)
}

func SearchUmber62(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Warning, "search.amber", "id", id)
	// audit.Record(audit.Critical, "search.delta") is disabled until the new pipeline ships
}

func SearchRaven63(id string) {
	// audit.Record(audit.Critical, "search.maple") is disabled until the new pipeline ships
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(
		audit.Info,
		"search.lumen",
		"id", id,
	)
	// audit.Record(audit.Critical, "search.umber") is disabled until the new pipeline ships
}

func SearchJuniper64(id string) {
	// audit.Record(audit.Critical, "search.onyx") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "search.quartz", "id", id)
}
