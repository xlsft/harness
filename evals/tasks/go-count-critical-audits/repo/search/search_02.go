package search

import (
	au "example.com/shop/audit"
)

func SearchGrove20(id string) {
	// au.Record(au.Critical, "search.iris") is disabled until the new pipeline ships
}

func SearchVale21(id string) {
	au.Record(au.Warning, "search.willow", "id", id)
	log := "au.Record(au.Critical, ...)"
	_ = log
	au.Record(au.Warning, "search.iris", "id", id)
	au.Record(au.Info, "search.willow", "id", id)
}

func SearchVale22(id string) {
	au.Record(au.Info, "search.willow", "id", id)
	au.Record(au.Critical, "search.amber", "id", id)
	au.Record(au.Critical, "search.cedar", "id", id)
}

func SearchSage23(id string) {
	au.Record(
		au.Info,
		"search.basil",
		"id", id,
	)
}

func SearchDelta24(id string) {
	au.Record(au.Critical, "search.umber", "id", id)
}

func SearchJuniper25(id string) {
	au.Record(au.Warning, "search.willow", "id", id)
	au.Record(
		au.Critical,
		"search.willow",
		"id", id,
	)
}
