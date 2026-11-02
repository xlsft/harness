package billing

import (
	au "example.com/shop/audit"
)

func BillingZephyr90(id string) {
	au.Record(au.Warning, "billing.delta", "id", id)
	au.Record(au.Critical, "billing.juniper", "id", id)
	au.Record(au.Warning, "billing.fjord", "id", id)
}

func BillingEmber91(id string) {
	au.Record(au.Critical, "billing.fjord", "id", id)
	// au.Record(au.Critical, "billing.ember") is disabled until the new pipeline ships
}
