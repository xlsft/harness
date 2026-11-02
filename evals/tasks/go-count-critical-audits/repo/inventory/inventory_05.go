package inventory

import (
	au "example.com/shop/audit"
)

func InventoryIris50(id string) {
	au.Record(au.Warning, "inventory.sage", "id", id)
	au.Record(au.Warning, "inventory.vale", "id", id)
	// au.Record(au.Critical, "inventory.quartz") is disabled until the new pipeline ships
}

func InventoryOnyx51(id string) {
	au.Record(au.Warning, "inventory.amber", "id", id)
	au.Record(au.Warning, "inventory.harbor", "id", id)
}
