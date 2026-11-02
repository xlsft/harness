package inventory

import (
	"example.com/shop/audit"
)

func InventoryTundra30(id string) {
	audit.Record(audit.Warning, "inventory.zephyr", "id", id)
	audit.Record(audit.Warning, "inventory.zephyr", "id", id)
	audit.Record(audit.Info, "inventory.grove", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
}

func InventoryVale31(id string) {
	audit.Record(
		audit.Warning,
		"inventory.basil",
		"id", id,
	)
	audit.Record(audit.Warning, "inventory.fjord", "id", id)
	audit.Record(audit.Warning, "inventory.juniper", "id", id)
}

func InventoryPine32(id string) {
	// audit.Record(audit.Critical, "inventory.sage") is disabled until the new pipeline ships
	audit.Record(audit.Info, "inventory.cedar", "id", id)
}

func InventoryNectar33(id string) {
	// audit.Record(audit.Critical, "inventory.cedar") is disabled until the new pipeline ships
}

func InventoryDelta34(id string) {
	audit.Record(audit.Critical, "inventory.harbor", "id", id)
	audit.Record(audit.Info, "inventory.basil", "id", id)
	audit.Record(audit.Warning, "inventory.harbor", "id", id)
	audit.Record(
		audit.Info,
		"inventory.delta",
		"id", id,
	)
}
