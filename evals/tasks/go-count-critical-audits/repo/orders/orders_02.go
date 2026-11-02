package orders

import (
	"example.com/shop/audit"
)

func OrdersYarrow20(id string) {
	audit.Record(audit.Info, "orders.tundra", "id", id)
	audit.Record(audit.Info, "orders.vale", "id", id)
	audit.Record(
		audit.Critical,
		"orders.kelp",
		"id", id,
	)
	audit.Record(audit.Info, "orders.pine", "id", id)
}

func OrdersGrove21(id string) {
	audit.Record(audit.Warning, "orders.delta", "id", id)
	audit.Record(audit.Info, "orders.harbor", "id", id)
}

func OrdersAmber22(id string) {
	audit.Record(
		audit.Info,
		"orders.lumen",
		"id", id,
	)
}
