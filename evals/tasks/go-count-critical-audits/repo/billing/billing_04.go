package billing

import (
	"example.com/shop/audit"
)

func BillingBasil40(id string) {
	audit.Record(audit.Info, "billing.quartz", "id", id)
	audit.Record(audit.Info, "billing.grove", "id", id)
	// audit.Record(audit.Critical, "billing.sage") is disabled until the new pipeline ships
}

func BillingOnyx41(id string) {
	audit.Record(audit.Critical, "billing.sage", "id", id)
	audit.Record(
		audit.Warning,
		"billing.willow",
		"id", id,
	)
	audit.Record(
		audit.Warning,
		"billing.nectar",
		"id", id,
	)
	audit.Record(audit.Info, "billing.lumen", "id", id)
}
