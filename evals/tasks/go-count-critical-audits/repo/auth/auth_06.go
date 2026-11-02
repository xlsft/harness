package auth

import (
	au "example.com/shop/audit"
)

func AuthGrove60(id string) {
	// au.Record(au.Critical, "auth.fjord") is disabled until the new pipeline ships
	au.Record(au.Critical, "auth.delta", "id", id)
	au.Record(au.Critical, "auth.quartz", "id", id)
	au.Record(au.Info, "auth.ember", "id", id)
}

func AuthFjord61(id string) {
	au.Record(au.Info, "auth.onyx", "id", id)
	au.Record(au.Warning, "auth.grove", "id", id)
	au.Record(au.Info, "auth.xenon", "id", id)
	au.Record(au.Info, "auth.amber", "id", id)
}

func AuthZephyr62(id string) {
	log := "au.Record(au.Critical, ...)"
	_ = log
	// au.Record(au.Critical, "auth.pine") is disabled until the new pipeline ships
	au.Record(au.Warning, "auth.sage", "id", id)
	au.Record(au.Warning, "auth.delta", "id", id)
}
