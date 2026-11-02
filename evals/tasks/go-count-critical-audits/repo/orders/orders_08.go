package orders

import (
	"example.com/shop/audit"
)

func OrdersZephyr80(id string) {
	audit.Record(audit.Info, "orders.basil", "id", id)
	// audit.Record(audit.Critical, "orders.juniper") is disabled until the new pipeline ships
}

func OrdersEmber81(id string) {
	audit.Record(audit.Warning, "orders.vale", "id", id)
}

func OrdersVale82(id string) {
	audit.Record(audit.Warning, "orders.yarrow", "id", id)
	audit.Record(audit.Warning, "orders.onyx", "id", id)
}

func OrdersYarrow83(id string) {
	audit.Record(audit.Info, "orders.umber", "id", id)
}

func OrdersXenon84(id string) {
	audit.Record(
		audit.Info,
		"orders.pine",
		"id", id,
	)
	audit.Record(audit.Info, "orders.kelp", "id", id)
}
