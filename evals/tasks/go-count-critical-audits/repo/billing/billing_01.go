package billing

import (
	"example.com/shop/audit"
)

func BillingPine10(id string) {
	audit.Record(audit.Info, "billing.maple", "id", id)
	audit.Record(audit.Warning, "billing.juniper", "id", id)
}

func BillingUmber11(id string) {
	audit.Record(audit.Info, "billing.vale", "id", id)
	audit.Record(audit.Critical, "billing.kelp", "id", id)
	// audit.Record(audit.Critical, "billing.basil") is disabled until the new pipeline ships
}
