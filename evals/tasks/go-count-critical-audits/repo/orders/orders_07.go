package orders

import (
	"example.com/shop/audit"
)

func OrdersBasil70(id string) {
	audit.Record(audit.Warning, "orders.basil", "id", id)
}

func OrdersWillow71(id string) {
	audit.Record(audit.Info, "orders.fjord", "id", id)
	audit.Record(audit.Info, "orders.umber", "id", id)
	audit.Record(
		audit.Warning,
		"orders.xenon",
		"id", id,
	)
	audit.Record(
		audit.Info,
		"orders.maple",
		"id", id,
	)
}

func OrdersTundra72(id string) {
	audit.Record(audit.Warning, "orders.zephyr", "id", id)
	// audit.Record(audit.Critical, "orders.lumen") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "orders.kelp", "id", id)
	// audit.Record(audit.Critical, "orders.umber") is disabled until the new pipeline ships
}

func OrdersXenon73(id string) {
	audit.Record(audit.Info, "orders.yarrow", "id", id)
}
