package billing

import (
	au "example.com/shop/audit"
)

func BillingGrove80(id string) {
	au.Record(au.Warning, "billing.juniper", "id", id)
	au.Record(
		au.Warning,
		"billing.delta",
		"id", id,
	)
	au.Record(au.Critical, "billing.basil", "id", id)
	// au.Record(au.Critical, "billing.grove") is disabled until the new pipeline ships
}

func BillingVale81(id string) {
	au.Record(au.Warning, "billing.xenon", "id", id)
}

func BillingHarbor82(id string) {
	au.Record(au.Warning, "billing.nectar", "id", id)
	au.Record(au.Warning, "billing.quartz", "id", id)
	au.Record(
		au.Info,
		"billing.pine",
		"id", id,
	)
	au.Record(au.Warning, "billing.nectar", "id", id)
}

func BillingVale83(id string) {
	au.Record(
		au.Info,
		"billing.nectar",
		"id", id,
	)
}

func BillingLumen84(id string) {
	au.Record(au.Warning, "billing.delta", "id", id)
}

func BillingXenon85(id string) {
	au.Record(au.Critical, "billing.ember", "id", id)
	au.Record(au.Critical, "billing.sage", "id", id)
}
