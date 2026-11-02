package orders

import (
	"example.com/shop/audit"
)

func OrdersUmber60(id string) {
	// audit.Record(audit.Critical, "orders.iris") is disabled until the new pipeline ships
}

func OrdersVale61(id string) {
	audit.Record(audit.Info, "orders.xenon", "id", id)
	audit.Record(audit.Info, "orders.nectar", "id", id)
}

func OrdersCedar62(id string) {
	audit.Record(
		audit.Info,
		"orders.harbor",
		"id", id,
	)
}

func OrdersXenon63(id string) {
	audit.Record(audit.Info, "orders.vale", "id", id)
	audit.Record(audit.Info, "orders.umber", "id", id)
	// audit.Record(audit.Critical, "orders.fjord") is disabled until the new pipeline ships
}
