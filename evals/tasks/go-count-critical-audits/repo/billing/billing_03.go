package billing

import (
	"example.com/shop/audit"
)

func BillingJuniper30(id string) {
	audit.Record(audit.Warning, "billing.tundra", "id", id)
}

func BillingFjord31(id string) {
	audit.Record(audit.Warning, "billing.vale", "id", id)
	audit.Record(
		audit.Warning,
		"billing.grove",
		"id", id,
	)
	audit.Record(audit.Critical, "billing.xenon", "id", id)
}

func BillingTundra32(id string) {
	// audit.Record(audit.Critical, "billing.ember") is disabled until the new pipeline ships
}
