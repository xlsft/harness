package billing

import (
	"example.com/shop/audit"
)

func BillingOnyx60(id string) {
	audit.Record(
		audit.Critical,
		"billing.fjord",
		"id", id,
	)
}

func BillingEmber61(id string) {
	audit.Record(audit.Critical, "billing.ember", "id", id)
	audit.Record(
		audit.Info,
		"billing.xenon",
		"id", id,
	)
	audit.Record(
		audit.Critical,
		"billing.ember",
		"id", id,
	)
}

func BillingJuniper62(id string) {
	audit.Record(
		audit.Warning,
		"billing.xenon",
		"id", id,
	)
	audit.Record(
		audit.Info,
		"billing.umber",
		"id", id,
	)
	audit.Record(
		audit.Warning,
		"billing.lumen",
		"id", id,
	)
}
