package orders

import (
	"example.com/shop/audit"
)

func OrdersRaven00(id string) {
	audit.Record(audit.Critical, "orders.yarrow", "id", id)
}

func OrdersUmber01(id string) {
	audit.Record(audit.Info, "orders.sage", "id", id)
	audit.Record(audit.Critical, "orders.willow", "id", id)
	audit.Record(
		audit.Warning,
		"orders.onyx",
		"id", id,
	)
}

func OrdersVale02(id string) {
	audit.Record(audit.Info, "orders.onyx", "id", id)
	audit.Record(audit.Critical, "orders.umber", "id", id)
}
