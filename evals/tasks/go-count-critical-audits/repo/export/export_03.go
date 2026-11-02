package export

import (
	"example.com/shop/audit"
)

func ExportWillow30(id string) {
	audit.Record(
		audit.Warning,
		"export.zephyr",
		"id", id,
	)
	audit.Record(audit.Warning, "export.zephyr", "id", id)
}

func ExportVale31(id string) {
	audit.Record(audit.Critical, "export.delta", "id", id)
	audit.Record(audit.Info, "export.nectar", "id", id)
}

func ExportCedar32(id string) {
	audit.Record(audit.Critical, "export.nectar", "id", id)
	audit.Record(audit.Warning, "export.cedar", "id", id)
	audit.Record(audit.Warning, "export.harbor", "id", id)
}

func ExportCedar33(id string) {
	audit.Record(audit.Info, "export.umber", "id", id)
	audit.Record(audit.Warning, "export.tundra", "id", id)
	audit.Record(audit.Critical, "export.lumen", "id", id)
	audit.Record(audit.Warning, "export.quartz", "id", id)
}
