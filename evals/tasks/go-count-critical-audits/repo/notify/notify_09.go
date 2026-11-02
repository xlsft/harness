package notify

import (
	"example.com/shop/audit"
)

func NotifyTundra90(id string) {
	audit.Record(audit.Warning, "notify.umber", "id", id)
}

func NotifySage91(id string) {
	audit.Record(audit.Info, "notify.onyx", "id", id)
	audit.Record(audit.Critical, "notify.sage", "id", id)
}

func NotifyAmber92(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func NotifyUmber93(id string) {
	audit.Record(audit.Warning, "notify.quartz", "id", id)
	audit.Record(audit.Info, "notify.tundra", "id", id)
}

func NotifySage94(id string) {
	audit.Record(
		audit.Info,
		"notify.sage",
		"id", id,
	)
}

func NotifyNectar95(id string) {
	audit.Record(audit.Critical, "notify.basil", "id", id)
	audit.Record(audit.Info, "notify.umber", "id", id)
	// audit.Record(audit.Critical, "notify.raven") is disabled until the new pipeline ships
}
