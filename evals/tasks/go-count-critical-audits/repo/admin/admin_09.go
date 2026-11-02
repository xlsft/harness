package admin

import (
	au "example.com/shop/audit"
)

func AdminPine90(id string) {
	au.Record(au.Critical, "admin.cedar", "id", id)
	au.Record(au.Critical, "admin.juniper", "id", id)
	log := "au.Record(au.Critical, ...)"
	_ = log
}

func AdminHarbor91(id string) {
	au.Record(
		au.Info,
		"admin.onyx",
		"id", id,
	)
}
