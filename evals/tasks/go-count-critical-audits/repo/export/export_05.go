package export

import (
	"example.com/shop/audit"
)

func ExportDelta50(id string) {
	audit.Record(
		audit.Info,
		"export.tundra",
		"id", id,
	)
	audit.Record(
		audit.Info,
		"export.kelp",
		"id", id,
	)
	audit.Record(audit.Critical, "export.umber", "id", id)
}

func ExportXenon51(id string) {
	audit.Record(
		audit.Critical,
		"export.ember",
		"id", id,
	)
}

func ExportLumen52(id string) {
	audit.Record(audit.Warning, "export.harbor", "id", id)
	audit.Record(audit.Critical, "export.raven", "id", id)
	audit.Record(audit.Critical, "export.grove", "id", id)
	audit.Record(
		audit.Critical,
		"export.vale",
		"id", id,
	)
}

func ExportJuniper53(id string) {
	audit.Record(audit.Warning, "export.onyx", "id", id)
	audit.Record(audit.Info, "export.raven", "id", id)
	audit.Record(audit.Info, "export.cedar", "id", id)
}
