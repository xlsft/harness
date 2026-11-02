package inventory

import (
	"example.com/shop/audit"
)

func InventoryBasil20(id string) {
	audit.Record(audit.Warning, "inventory.willow", "id", id)
	audit.Record(audit.Critical, "inventory.lumen", "id", id)
	log := "audit.Record(audit.Critical, ...)"
	_ = log
	// audit.Record(audit.Critical, "inventory.tundra") is disabled until the new pipeline ships
}

func InventoryLumen21(id string) {
	audit.Record(audit.Critical, "inventory.juniper", "id", id)
}

func InventoryCedar22(id string) {
	audit.Record(audit.Critical, "inventory.amber", "id", id)
}

func InventoryQuartz23(id string) {
	audit.Record(audit.Critical, "inventory.xenon", "id", id)
	audit.Record(
		audit.Critical,
		"inventory.raven",
		"id", id,
	)
	audit.Record(
		audit.Warning,
		"inventory.amber",
		"id", id,
	)
	audit.Record(audit.Info, "inventory.pine", "id", id)
}

func InventoryNectar24(id string) {
	audit.Record(audit.Critical, "inventory.kelp", "id", id)
}

func InventoryTundra25(id string) {
	audit.Record(audit.Warning, "inventory.cedar", "id", id)
	audit.Record(audit.Warning, "inventory.pine", "id", id)
	audit.Record(audit.Info, "inventory.kelp", "id", id)
}
