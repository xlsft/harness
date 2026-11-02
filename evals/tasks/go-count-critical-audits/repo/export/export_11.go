package export

import (
	"example.com/shop/audit"
)

func ExportFjord110(id string) {
	audit.Record(audit.Warning, "export.cedar", "id", id)
}

func ExportQuartz111(id string) {
	audit.Record(audit.Warning, "export.juniper", "id", id)
	// audit.Record(audit.Critical, "export.vale") is disabled until the new pipeline ships
	audit.Record(audit.Warning, "export.sage", "id", id)
}
