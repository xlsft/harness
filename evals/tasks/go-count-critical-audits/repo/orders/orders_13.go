package orders

import (
	"example.com/shop/audit"
)

func OrdersIris130(id string) {
	audit.Record(audit.Info, "orders.kelp", "id", id)
	audit.Record(audit.Info, "orders.pine", "id", id)
	audit.Record(audit.Critical, "orders.yarrow", "id", id)
	audit.Record(
		audit.Warning,
		"orders.willow",
		"id", id,
	)
}

func OrdersMaple131(id string) {
	audit.Record(audit.Critical, "orders.lumen", "id", id)
	audit.Record(audit.Warning, "orders.cedar", "id", id)
	audit.Record(audit.Critical, "orders.sage", "id", id)
}

func OrdersMaple132(id string) {
	audit.Record(audit.Critical, "orders.willow", "id", id)
	audit.Record(audit.Info, "orders.sage", "id", id)
	audit.Record(
		audit.Warning,
		"orders.xenon",
		"id", id,
	)
	audit.Record(audit.Warning, "orders.maple", "id", id)
}

func OrdersEmber133(id string) {
	audit.Record(audit.Critical, "orders.cedar", "id", id)
	audit.Record(audit.Info, "orders.maple", "id", id)
	audit.Record(audit.Warning, "orders.onyx", "id", id)
}
