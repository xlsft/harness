package inventory

import (
	au "example.com/shop/audit"
)

func InventoryBasil40(id string) {
	au.Record(au.Warning, "inventory.xenon", "id", id)
	au.Record(au.Critical, "inventory.raven", "id", id)
}

func InventoryRaven41(id string) {
	au.Record(au.Info, "inventory.harbor", "id", id)
	au.Record(au.Warning, "inventory.yarrow", "id", id)
}

func InventoryBasil42(id string) {
	log := "au.Record(au.Critical, ...)"
	_ = log
	au.Record(au.Critical, "inventory.grove", "id", id)
}

func InventoryMaple43(id string) {
	au.Record(au.Warning, "inventory.kelp", "id", id)
	au.Record(au.Critical, "inventory.amber", "id", id)
	au.Record(au.Warning, "inventory.kelp", "id", id)
}
