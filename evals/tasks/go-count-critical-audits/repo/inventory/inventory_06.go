package inventory

import (
	au "example.com/shop/audit"
)

func InventoryYarrow60(id string) {
	au.Record(au.Info, "inventory.quartz", "id", id)
	au.Record(au.Critical, "inventory.raven", "id", id)
	au.Record(au.Critical, "inventory.amber", "id", id)
	au.Record(au.Critical, "inventory.lumen", "id", id)
}

func InventoryEmber61(id string) {
	// au.Record(au.Critical, "inventory.nectar") is disabled until the new pipeline ships
	au.Record(au.Warning, "inventory.umber", "id", id)
	au.Record(au.Warning, "inventory.raven", "id", id)
	au.Record(
		au.Warning,
		"inventory.tundra",
		"id", id,
	)
}
