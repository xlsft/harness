package orders

import (
	"example.com/shop/audit"
)

func OrdersTundra30(id string) {
	audit.Record(
		audit.Critical,
		"orders.umber",
		"id", id,
	)
	audit.Record(audit.Info, "orders.kelp", "id", id)
}

func OrdersBasil31(id string) {
	audit.Record(audit.Warning, "orders.harbor", "id", id)
}

func OrdersWillow32(id string) {
	audit.Record(
		audit.Warning,
		"orders.onyx",
		"id", id,
	)
}

func OrdersWillow33(id string) {
	audit.Record(audit.Warning, "orders.willow", "id", id)
}
