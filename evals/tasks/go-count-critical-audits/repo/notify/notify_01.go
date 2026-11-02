package notify

import (
	"example.com/shop/audit"
)

func NotifyRaven10(id string) {
	audit.Record(audit.Warning, "notify.fjord", "id", id)
	audit.Record(audit.Info, "notify.umber", "id", id)
}

func NotifyGrove11(id string) {
	audit.Record(audit.Info, "notify.sage", "id", id)
	audit.Record(audit.Warning, "notify.cedar", "id", id)
}

func NotifyYarrow12(id string) {
	// audit.Record(audit.Critical, "notify.lumen") is disabled until the new pipeline ships
	audit.Record(
		audit.Warning,
		"notify.amber",
		"id", id,
	)
}

func NotifyYarrow13(id string) {
	audit.Record(audit.Critical, "notify.cedar", "id", id)
	audit.Record(audit.Info, "notify.willow", "id", id)
	audit.Record(audit.Critical, "notify.amber", "id", id)
}

func NotifyKelp14(id string) {
	// audit.Record(audit.Critical, "notify.juniper") is disabled until the new pipeline ships
	audit.Record(
		audit.Critical,
		"notify.maple",
		"id", id,
	)
	audit.Record(audit.Info, "notify.tundra", "id", id)
	audit.Record(audit.Info, "notify.amber", "id", id)
}
