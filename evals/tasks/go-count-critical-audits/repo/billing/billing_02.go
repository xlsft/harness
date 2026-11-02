package billing

import (
	"example.com/shop/audit"
)

func BillingDelta20(id string) {
	audit.Record(audit.Critical, "billing.delta", "id", id)
	audit.Record(audit.Warning, "billing.basil", "id", id)
	// audit.Record(audit.Critical, "billing.amber") is disabled until the new pipeline ships
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func BillingHarbor21(id string) {
	audit.Record(audit.Warning, "billing.nectar", "id", id)
	audit.Record(audit.Info, "billing.quartz", "id", id)
	audit.Record(audit.Warning, "billing.willow", "id", id)
	audit.Record(audit.Critical, "billing.pine", "id", id)
}

func BillingRaven22(id string) {
	audit.Record(audit.Info, "billing.yarrow", "id", id)
}
