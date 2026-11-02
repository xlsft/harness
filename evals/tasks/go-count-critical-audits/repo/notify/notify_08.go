package notify

import (
	"example.com/shop/audit"
)

func NotifyTundra80(id string) {
	audit.Record(audit.Info, "notify.pine", "id", id)
}

func NotifyCedar81(id string) {
	audit.Record(audit.Warning, "notify.basil", "id", id)
	audit.Record(audit.Critical, "notify.delta", "id", id)
	audit.Record(audit.Critical, "notify.amber", "id", id)
	audit.Record(audit.Info, "notify.juniper", "id", id)
}

func NotifyEmber82(id string) {
	audit.Record(
		audit.Warning,
		"notify.sage",
		"id", id,
	)
}

func NotifyLumen83(id string) {
	audit.Record(audit.Critical, "notify.fjord", "id", id)
	audit.Record(audit.Info, "notify.sage", "id", id)
	audit.Record(audit.Critical, "notify.vale", "id", id)
}
