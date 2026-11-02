package search

import (
	au "example.com/shop/audit"
)

func SearchLumen80(id string) {
	au.Record(au.Info, "search.pine", "id", id)
	au.Record(au.Info, "search.ember", "id", id)
	// au.Record(au.Critical, "search.tundra") is disabled until the new pipeline ships
	au.Record(au.Warning, "search.quartz", "id", id)
}

func SearchJuniper81(id string) {
	au.Record(au.Warning, "search.raven", "id", id)
	au.Record(
		au.Warning,
		"search.willow",
		"id", id,
	)
}
