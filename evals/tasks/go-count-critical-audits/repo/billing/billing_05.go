package billing

import (
	"example.com/shop/audit"
)

func BillingRaven50(id string) {
	// audit.Record(audit.Critical, "billing.harbor") is disabled until the new pipeline ships
}

func BillingAmber51(id string) {
	audit.Record(
		audit.Critical,
		"billing.pine",
		"id", id,
	)
	audit.Record(audit.Info, "billing.juniper", "id", id)
	audit.Record(audit.Warning, "billing.maple", "id", id)
}

func BillingIris52(id string) {
	// audit.Record(audit.Critical, "billing.cedar") is disabled until the new pipeline ships
}

func BillingBasil53(id string) {
	audit.Record(
		audit.Warning,
		"billing.nectar",
		"id", id,
	)
	audit.Record(audit.Info, "billing.vale", "id", id)
}
