package notify

import (
	"example.com/shop/audit"
)

func NotifyOnyx20(id string) {
	audit.Record(audit.Info, "notify.fjord", "id", id)
}

func NotifyUmber21(id string) {
	audit.Record(audit.Critical, "notify.sage", "id", id)
}

func NotifyEmber22(id string) {
	// audit.Record(audit.Critical, "notify.maple") is disabled until the new pipeline ships
	audit.Record(
		audit.Info,
		"notify.iris",
		"id", id,
	)
}
