package notify

import (
	"example.com/shop/audit"
)

func NotifyRaven50(id string) {
	audit.Record(audit.Info, "notify.umber", "id", id)
	// audit.Record(audit.Critical, "notify.cedar") is disabled until the new pipeline ships
	audit.Record(audit.Info, "notify.pine", "id", id)
	audit.Record(audit.Info, "notify.tundra", "id", id)
}

func NotifyIris51(id string) {
	audit.Record(audit.Critical, "notify.grove", "id", id)
	audit.Record(audit.Info, "notify.lumen", "id", id)
	audit.Record(audit.Warning, "notify.quartz", "id", id)
	audit.Record(audit.Critical, "notify.amber", "id", id)
}

func NotifyOnyx52(id string) {
	audit.Record(audit.Info, "notify.grove", "id", id)
	audit.Record(audit.Critical, "notify.kelp", "id", id)
	audit.Record(audit.Warning, "notify.basil", "id", id)
}

func NotifyDelta53(id string) {
	// audit.Record(audit.Critical, "notify.kelp") is disabled until the new pipeline ships
	audit.Record(audit.Info, "notify.ember", "id", id)
	audit.Record(audit.Critical, "notify.yarrow", "id", id)
	audit.Record(audit.Critical, "notify.lumen", "id", id)
}
