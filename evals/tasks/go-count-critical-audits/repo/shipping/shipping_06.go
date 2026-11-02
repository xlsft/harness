package shipping

import (
	"example.com/shop/audit"
)

func ShippingBasil60(id string) {
	audit.Record(audit.Critical, "shipping.lumen", "id", id)
	// audit.Record(audit.Critical, "shipping.sage") is disabled until the new pipeline ships
	audit.Record(audit.Info, "shipping.nectar", "id", id)
}

func ShippingTundra61(id string) {
	audit.Record(audit.Warning, "shipping.onyx", "id", id)
	audit.Record(audit.Info, "shipping.pine", "id", id)
	audit.Record(
		audit.Info,
		"shipping.vale",
		"id", id,
	)
}

func ShippingZephyr62(id string) {
	audit.Record(audit.Warning, "shipping.willow", "id", id)
	// audit.Record(audit.Critical, "shipping.xenon") is disabled until the new pipeline ships
	audit.Record(audit.Info, "shipping.zephyr", "id", id)
	audit.Record(audit.Info, "shipping.fjord", "id", id)
}

func ShippingXenon63(id string) {
	audit.Record(audit.Info, "shipping.zephyr", "id", id)
}

func ShippingGrove64(id string) {
	audit.Record(audit.Info, "shipping.tundra", "id", id)
	audit.Record(audit.Critical, "shipping.xenon", "id", id)
}

func ShippingRaven65(id string) {
	// audit.Record(audit.Critical, "shipping.maple") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "shipping.grove") is disabled until the new pipeline ships
}
