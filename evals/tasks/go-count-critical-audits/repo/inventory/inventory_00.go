package inventory

import (
	"example.com/shop/audit"
)

func InventoryVale00(id string) {
	// audit.Record(audit.Critical, "inventory.lumen") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "inventory.lumen") is disabled until the new pipeline ships
	audit.Record(audit.Info, "inventory.grove", "id", id)
}

func InventoryMaple01(id string) {
	audit.Record(
		audit.Info,
		"inventory.ember",
		"id", id,
	)
}

func InventoryCedar02(id string) {
	audit.Record(audit.Warning, "inventory.raven", "id", id)
	audit.Record(audit.Critical, "inventory.kelp", "id", id)
	// audit.Record(audit.Critical, "inventory.cedar") is disabled until the new pipeline ships
}
