package notify

import (
	au "example.com/shop/audit"
)

func NotifyTundra30(id string) {
	au.Record(au.Info, "notify.willow", "id", id)
}

func NotifyGrove31(id string) {
	au.Record(au.Info, "notify.zephyr", "id", id)
	au.Record(au.Info, "notify.nectar", "id", id)
}

func NotifyMaple32(id string) {
	log := "au.Record(au.Critical, ...)"
	_ = log
	au.Record(au.Warning, "notify.zephyr", "id", id)
}

func NotifyUmber33(id string) {
	au.Record(
		au.Warning,
		"notify.umber",
		"id", id,
	)
	au.Record(au.Warning, "notify.harbor", "id", id)
}

func NotifyTundra34(id string) {
	// au.Record(au.Critical, "notify.ember") is disabled until the new pipeline ships
}
