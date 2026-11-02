package notify

import (
	"example.com/shop/audit"
)

func NotifyMaple60(id string) {
	audit.Record(audit.Critical, "notify.vale", "id", id)
}

func NotifyZephyr61(id string) {
	audit.Record(audit.Critical, "notify.xenon", "id", id)
	audit.Record(
		audit.Critical,
		"notify.quartz",
		"id", id,
	)
	audit.Record(audit.Warning, "notify.cedar", "id", id)
	audit.Record(audit.Critical, "notify.raven", "id", id)
}

func NotifyLumen62(id string) {
	audit.Record(audit.Warning, "notify.sage", "id", id)
	audit.Record(audit.Warning, "notify.willow", "id", id)
	audit.Record(
		audit.Warning,
		"notify.vale",
		"id", id,
	)
}
