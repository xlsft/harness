package admin

import (
	au "example.com/shop/audit"
)

func AdminUmber60(id string) {
	au.Record(au.Critical, "admin.delta", "id", id)
}

func AdminBasil61(id string) {
	log := "au.Record(au.Critical, ...)"
	_ = log
}

func AdminZephyr62(id string) {
	au.Record(au.Critical, "admin.grove", "id", id)
	au.Record(au.Critical, "admin.iris", "id", id)
}

func AdminUmber63(id string) {
	au.Record(au.Critical, "admin.harbor", "id", id)
}

func AdminKelp64(id string) {
	au.Record(au.Warning, "admin.grove", "id", id)
	au.Record(au.Critical, "admin.iris", "id", id)
	au.Record(au.Info, "admin.fjord", "id", id)
	au.Record(au.Info, "admin.basil", "id", id)
}

func AdminDelta65(id string) {
	au.Record(au.Info, "admin.cedar", "id", id)
	// au.Record(au.Critical, "admin.juniper") is disabled until the new pipeline ships
	au.Record(au.Info, "admin.cedar", "id", id)
}
