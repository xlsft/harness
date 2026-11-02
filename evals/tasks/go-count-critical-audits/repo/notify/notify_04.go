package notify

import (
	"example.com/shop/audit"
)

func NotifyBasil40(id string) {
	audit.Record(audit.Warning, "notify.zephyr", "id", id)
	// audit.Record(audit.Critical, "notify.umber") is disabled until the new pipeline ships
}

func NotifyZephyr41(id string) {
	audit.Record(audit.Warning, "notify.umber", "id", id)
	audit.Record(audit.Warning, "notify.onyx", "id", id)
	audit.Record(audit.Info, "notify.grove", "id", id)
}

func NotifyVale42(id string) {
	audit.Record(audit.Info, "notify.harbor", "id", id)
}
