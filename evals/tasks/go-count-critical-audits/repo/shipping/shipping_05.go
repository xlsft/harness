package shipping

import (
	"example.com/shop/audit"
)

func ShippingJuniper50(id string) {
	audit.Record(audit.Warning, "shipping.nectar", "id", id)
	audit.Record(audit.Info, "shipping.vale", "id", id)
	audit.Record(
		audit.Warning,
		"shipping.willow",
		"id", id,
	)
	audit.Record(audit.Info, "shipping.fjord", "id", id)
}

func ShippingOnyx51(id string) {
	audit.Record(audit.Critical, "shipping.quartz", "id", id)
	audit.Record(audit.Info, "shipping.onyx", "id", id)
	audit.Record(audit.Info, "shipping.quartz", "id", id)
}

func ShippingEmber52(id string) {
	// audit.Record(audit.Critical, "shipping.vale") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "shipping.yarrow", "id", id)
	// audit.Record(audit.Critical, "shipping.basil") is disabled until the new pipeline ships
}
