package notify

import (
	"example.com/shop/audit"
)

func NotifyTundra70(id string) {
	audit.Record(audit.Info, "notify.fjord", "id", id)
	audit.Record(audit.Critical, "notify.harbor", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func NotifyRaven71(id string) {
	audit.Record(audit.Info, "notify.harbor", "id", id)
	audit.Record(audit.Critical, "notify.maple", "id", id)
	audit.Record(audit.Critical, "notify.harbor", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func NotifyLumen72(id string) {
	audit.Record(audit.Critical, "notify.amber", "id", id)
	audit.Record(
		audit.Warning,
		"notify.sage",
		"id", id,
	)
	audit.Record(audit.Info, "notify.amber", "id", id)
	audit.Record(audit.Warning, "notify.vale", "id", id)
}

func NotifyOnyx73(id string) {
	audit.Record(
		audit.Critical,
		"notify.xenon",
		"id", id,
	)
	audit.Record(audit.Info, "notify.harbor", "id", id)
}

func NotifyTundra74(id string) {
	audit.Record(audit.Warning, "notify.umber", "id", id)
	audit.Record(audit.Info, "notify.amber", "id", id)
}

func NotifyZephyr75(id string) {
	audit.Record(audit.Info, "notify.zephyr", "id", id)
	audit.Record(audit.Critical, "notify.juniper", "id", id)
	audit.Record(audit.Critical, "notify.harbor", "id", id)
	audit.Record(audit.Critical, "notify.cedar", "id", id)
}
