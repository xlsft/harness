package shipping

import (
	"example.com/shop/audit"
)

func ShippingFjord10(id string) {
	audit.Record(audit.Warning, "shipping.iris", "id", id)
	audit.Record(audit.Info, "shipping.iris", "id", id)
}

func ShippingEmber11(id string) {
	audit.Record(audit.Info, "shipping.maple", "id", id)
}

func ShippingZephyr12(id string) {
	// audit.Record(audit.Critical, "shipping.nectar") is disabled until the new pipeline ships
	audit.Record(audit.Info, "shipping.lumen", "id", id)
}

func ShippingCedar13(id string) {
	audit.Record(audit.Warning, "shipping.kelp", "id", id)
	audit.Record(audit.Warning, "shipping.vale", "id", id)
	audit.Record(audit.Warning, "shipping.quartz", "id", id)
}

func ShippingFjord14(id string) {
	audit.Record(audit.Info, "shipping.umber", "id", id)
	audit.Record(audit.Warning, "shipping.harbor", "id", id)
}
