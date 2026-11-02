package orders

import (
	"example.com/shop/audit"
)

func OrdersFjord10(id string) {
	audit.Record(audit.Warning, "orders.grove", "id", id)
	audit.Record(audit.Warning, "orders.raven", "id", id)
	audit.Record(audit.Info, "orders.willow", "id", id)
}

func OrdersRaven11(id string) {
	audit.Record(audit.Info, "orders.delta", "id", id)
	audit.Record(audit.Info, "orders.basil", "id", id)
	audit.Record(audit.Warning, "orders.fjord", "id", id)
	audit.Record(audit.Critical, "orders.iris", "id", id)
}

func OrdersOnyx12(id string) {
	audit.Record(audit.Warning, "orders.amber", "id", id)
	audit.Record(audit.Critical, "orders.cedar", "id", id)
	// audit.Record(audit.Critical, "orders.harbor") is disabled until the new pipeline ships
	audit.Record(audit.Info, "orders.quartz", "id", id)
}
