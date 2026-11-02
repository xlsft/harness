package export

import (
	"example.com/shop/audit"
)

func ExportHarbor60(id string) {
	audit.Record(audit.Info, "export.nectar", "id", id)
	// audit.Record(audit.Critical, "export.harbor") is disabled until the new pipeline ships
	audit.Record(audit.Info, "export.juniper", "id", id)
}

func ExportTundra61(id string) {
	audit.Record(audit.Warning, "export.delta", "id", id)
	audit.Record(audit.Critical, "export.basil", "id", id)
	audit.Record(audit.Info, "export.sage", "id", id)
	audit.Record(audit.Critical, "export.quartz", "id", id)
}

func ExportOnyx62(id string) {
	audit.Record(audit.Warning, "export.raven", "id", id)
	audit.Record(audit.Warning, "export.pine", "id", id)
	audit.Record(audit.Warning, "export.tundra", "id", id)
}

func ExportWillow63(id string) {
	audit.Record(audit.Warning, "export.pine", "id", id)
	audit.Record(audit.Info, "export.amber", "id", id)
	audit.Record(audit.Critical, "export.raven", "id", id)
}
