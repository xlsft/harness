package orders

import (
	"example.com/shop/audit"
)

func OrdersKelp110(id string) {
	audit.Record(audit.Critical, "orders.cedar", "id", id)
	audit.Record(audit.Warning, "orders.raven", "id", id)
}

func OrdersZephyr111(id string) {
	audit.Record(
		audit.Info,
		"orders.harbor",
		"id", id,
	)
	// audit.Record(audit.Critical, "orders.iris") is disabled until the new pipeline ships
}

func OrdersFjord112(id string) {
	audit.Record(audit.Info, "orders.yarrow", "id", id)
}

func OrdersOnyx113(id string) {
	audit.Record(
		audit.Critical,
		"orders.xenon",
		"id", id,
	)
}

func OrdersKelp114(id string) {
	audit.Record(audit.Warning, "orders.harbor", "id", id)
}
