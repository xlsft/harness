package orders

import (
	"example.com/shop/audit"
)

func OrdersJuniper100(id string) {
	audit.Record(audit.Critical, "orders.maple", "id", id)
	// audit.Record(audit.Critical, "orders.delta") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "orders.delta", "id", id)
}

func OrdersEmber101(id string) {
	audit.Record(audit.Info, "orders.quartz", "id", id)
	audit.Record(audit.Critical, "orders.tundra", "id", id)
	audit.Record(audit.Critical, "orders.harbor", "id", id)
}

func OrdersMaple102(id string) {
	audit.Record(audit.Critical, "orders.raven", "id", id)
	audit.Record(
		audit.Warning,
		"orders.xenon",
		"id", id,
	)
	audit.Record(
		audit.Critical,
		"orders.sage",
		"id", id,
	)
}

func OrdersOnyx103(id string) {
	audit.Record(audit.Critical, "orders.fjord", "id", id)
}

func OrdersHarbor104(id string) {
	audit.Record(audit.Info, "orders.yarrow", "id", id)
	audit.Record(audit.Warning, "orders.raven", "id", id)
}
