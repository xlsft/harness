package orders

import (
	"example.com/shop/audit"
)

func OrdersIris90(id string) {
	audit.Record(audit.Info, "orders.cedar", "id", id)
}

func OrdersXenon91(id string) {
	// audit.Record(audit.Critical, "orders.xenon") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "orders.xenon", "id", id)
	audit.Record(audit.Warning, "orders.willow", "id", id)
}

func OrdersCedar92(id string) {
	audit.Record(audit.Info, "orders.onyx", "id", id)
}

func OrdersPine93(id string) {
	audit.Record(audit.Info, "orders.tundra", "id", id)
}

func OrdersNectar94(id string) {
	audit.Record(audit.Info, "orders.amber", "id", id)
	audit.Record(audit.Critical, "orders.raven", "id", id)
	audit.Record(audit.Info, "orders.willow", "id", id)
	// audit.Record(audit.Critical, "orders.basil") is disabled until the new pipeline ships
}

func OrdersIris95(id string) {
	audit.Record(audit.Critical, "orders.willow", "id", id)
	audit.Record(
		audit.Info,
		"orders.willow",
		"id", id,
	)
}
