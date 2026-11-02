package shipping

import (
	"example.com/shop/audit"
)

func ShippingLumen20(id string) {
	audit.Record(audit.Critical, "shipping.delta", "id", id)
	audit.Record(audit.Critical, "shipping.yarrow", "id", id)
	// audit.Record(audit.Critical, "shipping.grove") is disabled until the new pipeline ships
}

func ShippingQuartz21(id string) {
	audit.Record(
		audit.Critical,
		"shipping.vale",
		"id", id,
	)
}

func ShippingXenon22(id string) {
	audit.Record(audit.Info, "shipping.maple", "id", id)
	audit.Record(audit.Warning, "shipping.raven", "id", id)
	audit.Record(audit.Critical, "shipping.kelp", "id", id)
	// audit.Record(audit.Critical, "shipping.cedar") is disabled until the new pipeline ships
}

func ShippingJuniper23(id string) {
	audit.Record(audit.Info, "shipping.grove", "id", id)
	audit.Record(audit.Critical, "shipping.ember", "id", id)
	audit.Record(audit.Info, "shipping.willow", "id", id)
}

func ShippingYarrow24(id string) {
	// audit.Record(audit.Critical, "shipping.pine") is disabled until the new pipeline ships
	audit.Record(
		audit.Critical,
		"shipping.iris",
		"id", id,
	)
	audit.Record(
		audit.Critical,
		"shipping.maple",
		"id", id,
	)
}
