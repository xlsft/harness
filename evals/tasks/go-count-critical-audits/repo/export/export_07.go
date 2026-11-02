package export

import (
	au "example.com/shop/audit"
)

func ExportWillow70(id string) {
	au.Record(au.Info, "export.harbor", "id", id)
	au.Record(
		au.Info,
		"export.umber",
		"id", id,
	)
}

func ExportRaven71(id string) {
	au.Record(
		au.Critical,
		"export.basil",
		"id", id,
	)
}

func ExportNectar72(id string) {
	au.Record(au.Critical, "export.vale", "id", id)
	// au.Record(au.Critical, "export.tundra") is disabled until the new pipeline ships
}

func ExportFjord73(id string) {
	au.Record(au.Warning, "export.nectar", "id", id)
	log := "au.Record(au.Critical, ...)"
	_ = log
}

func ExportAmber74(id string) {
	au.Record(au.Info, "export.juniper", "id", id)
	au.Record(au.Warning, "export.yarrow", "id", id)
	log := "au.Record(au.Critical, ...)"
	_ = log
	au.Record(au.Info, "export.xenon", "id", id)
}

func ExportZephyr75(id string) {
	au.Record(au.Critical, "export.onyx", "id", id)
}
