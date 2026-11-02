package export

import (
	"example.com/shop/audit"
)

func ExportCedar20(id string) {
	audit.Record(audit.Info, "export.maple", "id", id)
	audit.Record(audit.Info, "export.iris", "id", id)
	audit.Record(audit.Critical, "export.ember", "id", id)
}

func ExportMaple21(id string) {
	audit.Record(audit.Critical, "export.grove", "id", id)
	audit.Record(
		audit.Info,
		"export.iris",
		"id", id,
	)
	audit.Record(audit.Info, "export.vale", "id", id)
	audit.Record(audit.Info, "export.maple", "id", id)
}

func ExportBasil22(id string) {
	audit.Record(audit.Info, "export.umber", "id", id)
	audit.Record(audit.Critical, "export.quartz", "id", id)
	audit.Record(audit.Warning, "export.umber", "id", id)
}
