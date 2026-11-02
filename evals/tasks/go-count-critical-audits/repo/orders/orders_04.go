package orders

import (
	"example.com/shop/audit"
)

func OrdersNectar40(id string) {
	audit.Record(audit.Info, "orders.ember", "id", id)
}

func OrdersLumen41(id string) {
	audit.Record(audit.Critical, "orders.juniper", "id", id)
	audit.Record(audit.Warning, "orders.basil", "id", id)
}

func OrdersUmber42(id string) {
	audit.Record(audit.Critical, "orders.umber", "id", id)
}

func OrdersMaple43(id string) {
	audit.Record(audit.Critical, "orders.onyx", "id", id)
	audit.Record(audit.Critical, "orders.maple", "id", id)
	audit.Record(audit.Warning, "orders.kelp", "id", id)
}

func OrdersKelp44(id string) {
	audit.Record(audit.Warning, "orders.ember", "id", id)
	audit.Record(
		audit.Info,
		"orders.raven",
		"id", id,
	)
	audit.Record(audit.Info, "orders.sage", "id", id)
}
