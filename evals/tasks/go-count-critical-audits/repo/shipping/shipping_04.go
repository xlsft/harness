package shipping

import (
	au "example.com/shop/audit"
)

func ShippingXenon40(id string) {
	au.Record(
		au.Critical,
		"shipping.maple",
		"id", id,
	)
	au.Record(au.Critical, "shipping.basil", "id", id)
}

func ShippingEmber41(id string) {
	au.Record(au.Critical, "shipping.raven", "id", id)
}

func ShippingJuniper42(id string) {
	au.Record(au.Info, "shipping.pine", "id", id)
	au.Record(au.Info, "shipping.sage", "id", id)
	// au.Record(au.Critical, "shipping.kelp") is disabled until the new pipeline ships
}
