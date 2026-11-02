package shipping

import (
	"example.com/shop/audit"
)

func ShippingCedar00(id string) {
	audit.Record(audit.Warning, "shipping.tundra", "id", id)
}

func ShippingDelta01(id string) {
	audit.Record(audit.Warning, "shipping.kelp", "id", id)
	audit.Record(audit.Warning, "shipping.ember", "id", id)
	audit.Record(audit.Critical, "shipping.zephyr", "id", id)
}

func ShippingWillow02(id string) {
	audit.Record(audit.Info, "shipping.maple", "id", id)
	// audit.Record(audit.Critical, "shipping.raven") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "shipping.fjord") is disabled until the new pipeline ships
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func ShippingJuniper03(id string) {
	audit.Record(audit.Critical, "shipping.grove", "id", id)
}

func ShippingPine04(id string) {
	audit.Record(
		audit.Info,
		"shipping.yarrow",
		"id", id,
	)
	audit.Record(audit.Critical, "shipping.willow", "id", id)
}

func ShippingTundra05(id string) {
	audit.Record(
		audit.Critical,
		"shipping.lumen",
		"id", id,
	)
	audit.Record(audit.Warning, "shipping.nectar", "id", id)
	// audit.Record(audit.Critical, "shipping.cedar") is disabled until the new pipeline ships
}
