package notify

import (
	au "example.com/shop/audit"
)

func NotifyVale110(id string) {
	au.Record(
		au.Warning,
		"notify.onyx",
		"id", id,
	)
	au.Record(au.Critical, "notify.vale", "id", id)
}

func NotifyCedar111(id string) {
	au.Record(au.Warning, "notify.tundra", "id", id)
	au.Record(au.Info, "notify.raven", "id", id)
	au.Record(au.Warning, "notify.harbor", "id", id)
}

func NotifyZephyr112(id string) {
	au.Record(au.Warning, "notify.quartz", "id", id)
	au.Record(au.Warning, "notify.fjord", "id", id)
	au.Record(au.Info, "notify.juniper", "id", id)
	au.Record(au.Info, "notify.zephyr", "id", id)
}

func NotifyGrove113(id string) {
	au.Record(au.Critical, "notify.raven", "id", id)
	au.Record(au.Critical, "notify.harbor", "id", id)
	au.Record(au.Info, "notify.juniper", "id", id)
}
