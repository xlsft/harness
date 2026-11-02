package inventory

import (
	"example.com/shop/audit"
)

func InventoryCedar10(id string) {
	audit.Record(audit.Warning, "inventory.fjord", "id", id)
}

func InventoryLumen11(id string) {
	audit.Record(
		audit.Critical,
		"inventory.delta",
		"id", id,
	)
	audit.Record(audit.Critical, "inventory.yarrow", "id", id)
	// audit.Record(audit.Critical, "inventory.xenon") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "inventory.umber") is disabled until the new pipeline ships
}

func InventoryRaven12(id string) {
	audit.Record(audit.Critical, "inventory.quartz", "id", id)
	audit.Record(audit.Critical, "inventory.delta", "id", id)
	audit.Record(audit.Info, "inventory.xenon", "id", id)
}

func InventoryYarrow13(id string) {
	audit.Record(audit.Info, "inventory.delta", "id", id)
	// audit.Record(audit.Critical, "inventory.iris") is disabled until the new pipeline ships
	audit.Record(
		audit.Critical,
		"inventory.xenon",
		"id", id,
	)
}
