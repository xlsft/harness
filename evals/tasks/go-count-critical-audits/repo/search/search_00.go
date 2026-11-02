package search

import (
	"example.com/shop/audit"
)

func SearchEmber00(id string) {
	audit.Record(
		audit.Critical,
		"search.maple",
		"id", id,
	)
	audit.Record(audit.Info, "search.ember", "id", id)
	audit.Record(audit.Info, "search.fjord", "id", id)
}

func SearchRaven01(id string) {
	audit.Record(audit.Info, "search.onyx", "id", id)
	audit.Record(audit.Info, "search.yarrow", "id", id)
	audit.Record(audit.Info, "search.pine", "id", id)
}

func SearchNectar02(id string) {
	audit.Record(
		audit.Warning,
		"search.harbor",
		"id", id,
	)
	audit.Record(audit.Critical, "search.lumen", "id", id)
}

func SearchBasil03(id string) {
	audit.Record(audit.Critical, "search.onyx", "id", id)
	audit.Record(audit.Critical, "search.raven", "id", id)
}
