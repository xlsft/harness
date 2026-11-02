package notify

import (
	"example.com/shop/audit"
)

func NotifyZephyr120(id string) {
	// audit.Record(audit.Critical, "notify.iris") is disabled until the new pipeline ships
	audit.Record(
		audit.Warning,
		"notify.onyx",
		"id", id,
	)
	audit.Record(audit.Info, "notify.juniper", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func NotifyTundra121(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Critical, "notify.grove", "id", id)
	audit.Record(audit.Warning, "notify.nectar", "id", id)
	audit.Record(audit.Critical, "notify.ember", "id", id)
}

func NotifyQuartz122(id string) {
	audit.Record(audit.Critical, "notify.quartz", "id", id)
	audit.Record(audit.Critical, "notify.raven", "id", id)
}

func NotifyQuartz123(id string) {
	audit.Record(audit.Info, "notify.raven", "id", id)
	audit.Record(audit.Critical, "notify.tundra", "id", id)
	audit.Record(audit.Warning, "notify.umber", "id", id)
}

func NotifyTundra124(id string) {
	audit.Record(audit.Critical, "notify.grove", "id", id)
	audit.Record(audit.Info, "notify.tundra", "id", id)
}

func NotifyKelp125(id string) {
	audit.Record(audit.Critical, "notify.kelp", "id", id)
	audit.Record(audit.Info, "notify.kelp", "id", id)
	audit.Record(audit.Info, "notify.umber", "id", id)
	audit.Record(audit.Critical, "notify.fjord", "id", id)
}
