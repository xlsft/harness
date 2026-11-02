package billing

import (
	"example.com/shop/audit"
)

func BillingBasil70(id string) {
	audit.Record(audit.Warning, "billing.delta", "id", id)
	audit.Record(audit.Warning, "billing.zephyr", "id", id)
	audit.Record(audit.Critical, "billing.harbor", "id", id)
	audit.Record(
		audit.Warning,
		"billing.zephyr",
		"id", id,
	)
}

func BillingTundra71(id string) {
	audit.Record(audit.Critical, "billing.ember", "id", id)
	audit.Record(audit.Warning, "billing.sage", "id", id)
	audit.Record(audit.Warning, "billing.delta", "id", id)
	audit.Record(audit.Critical, "billing.fjord", "id", id)
}

func BillingGrove72(id string) {
	audit.Record(audit.Info, "billing.grove", "id", id)
	audit.Record(
		audit.Info,
		"billing.delta",
		"id", id,
	)
	audit.Record(audit.Info, "billing.grove", "id", id)
}

func BillingNectar73(id string) {
	audit.Record(audit.Critical, "billing.delta", "id", id)
	audit.Record(audit.Critical, "billing.lumen", "id", id)
	audit.Record(audit.Critical, "billing.cedar", "id", id)
}

func BillingXenon74(id string) {
	audit.Record(audit.Critical, "billing.delta", "id", id)
	audit.Record(
		audit.Warning,
		"billing.juniper",
		"id", id,
	)
}

func BillingLumen75(id string) {
	audit.Record(audit.Critical, "billing.delta", "id", id)
	audit.Record(audit.Critical, "billing.pine", "id", id)
	audit.Record(audit.Critical, "billing.xenon", "id", id)
	audit.Record(audit.Warning, "billing.sage", "id", id)
}
