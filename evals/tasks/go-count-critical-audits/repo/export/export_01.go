package export

import (
	"example.com/shop/audit"
)

func ExportNectar10(id string) {
	audit.Record(audit.Warning, "export.willow", "id", id)
}

func ExportGrove11(id string) {
	// audit.Record(audit.Critical, "export.zephyr") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "export.sage") is disabled until the new pipeline ships
	audit.Record(audit.Info, "export.fjord", "id", id)
}
