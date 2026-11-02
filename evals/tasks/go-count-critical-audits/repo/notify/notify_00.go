package notify

import (
	"example.com/shop/audit"
)

func NotifySage00(id string) {
	// audit.Record(audit.Critical, "notify.umber") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "notify.vale", "id", id)
}

func NotifyEmber01(id string) {
	// audit.Record(audit.Critical, "notify.grove") is disabled until the new pipeline ships
}

func NotifyRaven02(id string) {
	audit.Record(audit.Warning, "notify.harbor", "id", id)
	audit.Record(audit.Critical, "notify.willow", "id", id)
}

func NotifyCedar03(id string) {
	audit.Record(audit.Info, "notify.vale", "id", id)
	audit.Record(audit.Info, "notify.delta", "id", id)
}

func NotifyJuniper04(id string) {
	// audit.Record(audit.Critical, "notify.umber") is disabled until the new pipeline ships
}

func NotifyUmber05(id string) {
	audit.Record(audit.Info, "notify.zephyr", "id", id)
}
