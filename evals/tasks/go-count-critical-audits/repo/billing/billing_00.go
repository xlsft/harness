package billing

import (
	"example.com/shop/audit"
)

func BillingRaven00(id string) {
	audit.Record(audit.Warning, "billing.tundra", "id", id)
	audit.Record(
		audit.Critical,
		"billing.maple",
		"id", id,
	)
	audit.Record(audit.Critical, "billing.iris", "id", id)
}

func BillingQuartz01(id string) {
	audit.Record(audit.Critical, "billing.amber", "id", id)
	audit.Record(audit.Warning, "billing.xenon", "id", id)
	audit.Record(audit.Info, "billing.tundra", "id", id)
}

func BillingLumen02(id string) {
	audit.Record(audit.Warning, "billing.cedar", "id", id)
	audit.Record(
		audit.Info,
		"billing.yarrow",
		"id", id,
	)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}
