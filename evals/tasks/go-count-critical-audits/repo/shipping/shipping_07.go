package shipping

import (
	"example.com/shop/audit"
)

func ShippingBasil70(id string) {
	audit.Record(audit.Info, "shipping.harbor", "id", id)
	audit.Record(audit.Critical, "shipping.onyx", "id", id)
}

func ShippingQuartz71(id string) {
	// audit.Record(audit.Critical, "shipping.zephyr") is disabled until the new pipeline ships
}

func ShippingBasil72(id string) {
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "shipping.cedar", "id", id)
	audit.Record(audit.Critical, "shipping.sage", "id", id)
}

func ShippingJuniper73(id string) {
	audit.Record(
		audit.Critical,
		"shipping.juniper",
		"id", id,
	)
}
