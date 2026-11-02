package export

import (
	"example.com/shop/audit"
)

func ExportAmber90(id string) {
	audit.Record(audit.Info, "export.raven", "id", id)
	audit.Record(
		audit.Warning,
		"export.pine",
		"id", id,
	)
}

func ExportSage91(id string) {
	audit.Record(audit.Info, "export.harbor", "id", id)
	// audit.Record(audit.Critical, "export.sage") is disabled until the new pipeline ships
}
