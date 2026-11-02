package shipping

import (
	"example.com/shop/audit"
)

func ShippingOnyx30(id string) {
	audit.Record(audit.Info, "shipping.zephyr", "id", id)
	audit.Record(audit.Warning, "shipping.yarrow", "id", id)
	audit.Record(
		audit.Info,
		"shipping.maple",
		"id", id,
	)
	audit.Record(audit.Warning, "shipping.onyx", "id", id)
}

func ShippingJuniper31(id string) {
	audit.Record(audit.Critical, "shipping.amber", "id", id)
	audit.Record(audit.Info, "shipping.pine", "id", id)
	audit.Record(audit.Info, "shipping.raven", "id", id)
}

func ShippingZephyr32(id string) {
	audit.Record(audit.Info, "shipping.grove", "id", id)
	audit.Record(
		audit.Info,
		"shipping.vale",
		"id", id,
	)
}

func ShippingDelta33(id string) {
	audit.Record(
		audit.Warning,
		"shipping.cedar",
		"id", id,
	)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	audit.Record(audit.Info, "shipping.harbor", "id", id)
}
