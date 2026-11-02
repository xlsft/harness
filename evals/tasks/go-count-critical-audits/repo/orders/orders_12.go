package orders

import (
	"example.com/shop/audit"
)

func OrdersMaple120(id string) {
	audit.Record(audit.Info, "orders.raven", "id", id)
	audit.Record(audit.Info, "orders.tundra", "id", id)
	audit.Record(audit.Warning, "orders.raven", "id", id)
	audit.Record(
		audit.Warning,
		"orders.iris",
		"id", id,
	)
}

func OrdersOnyx121(id string) {
	audit.Record(audit.Critical, "orders.nectar", "id", id)
	// audit.Record(audit.Critical, "orders.xenon") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "orders.lumen", "id", id)
}

func OrdersIris122(id string) {
	audit.Record(audit.Critical, "orders.onyx", "id", id)
	audit.Record(audit.Critical, "orders.nectar", "id", id)
	audit.Record(audit.Critical, "orders.sage", "id", id)
	audit.Record(
		audit.Info,
		"orders.ember",
		"id", id,
	)
}

func OrdersVale123(id string) {
	audit.Record(audit.Warning, "orders.sage", "id", id)
	audit.Record(audit.Critical, "orders.umber", "id", id)
}

func OrdersRaven124(id string) {
	// audit.Record(audit.Critical, "orders.basil") is disabled until the new pipeline ships
	audit.Record(
		audit.Info,
		"orders.tundra",
		"id", id,
	)
}

func OrdersMaple125(id string) {
	audit.Record(audit.Info, "orders.umber", "id", id)
}
