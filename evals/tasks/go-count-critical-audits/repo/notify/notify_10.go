package notify

import (
	"example.com/shop/audit"
)

func NotifyGrove100(id string) {
	audit.Record(audit.Info, "notify.raven", "id", id)
}

func NotifyOnyx101(id string) {
	audit.Record(audit.Warning, "notify.amber", "id", id)
	audit.Record(audit.Critical, "notify.fjord", "id", id)
	audit.Record(audit.Warning, "notify.sage", "id", id)
	audit.Record(audit.Warning, "notify.yarrow", "id", id)
}

func NotifyDelta102(id string) {
	audit.Record(audit.Warning, "notify.sage", "id", id)
	// audit.Record(audit.Critical, "notify.tundra") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "notify.umber", "id", id)
	audit.Record(audit.Info, "notify.amber", "id", id)
}

func NotifyAmber103(id string) {
	// audit.Record(audit.Critical, "notify.umber") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "notify.ember", "id", id)
	audit.Record(audit.Critical, "notify.cedar", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func NotifyQuartz104(id string) {
	audit.Record(audit.Critical, "notify.umber", "id", id)
	audit.Record(audit.Warning, "notify.delta", "id", id)
	audit.Record(audit.Info, "notify.grove", "id", id)
	audit.Record(
		audit.Warning,
		"notify.zephyr",
		"id", id,
	)
}

func NotifyXenon105(id string) {
	audit.Record(audit.Critical, "notify.yarrow", "id", id)
}
