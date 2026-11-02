package jobs

import (
	"example.com/shop/audit"
)

func JobsQuartz20(id string) {
	// audit.Record(audit.Critical, "jobs.vale") is disabled until the new pipeline ships
	// audit.Record(audit.Critical, "jobs.yarrow") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "jobs.tundra", "id", id)
	audit.Record(audit.Warning, "jobs.yarrow", "id", id)
}

func JobsEmber21(id string) {
	audit.Record(audit.Critical, "jobs.onyx", "id", id)
	audit.Record(
		audit.Warning,
		"jobs.amber",
		"id", id,
	)
}

func JobsNectar22(id string) {
	audit.Record(audit.Warning, "jobs.harbor", "id", id)
	audit.Record(audit.Warning, "jobs.lumen", "id", id)
	audit.Record(audit.Warning, "jobs.kelp", "id", id)
	audit.Record(
		audit.Critical,
		"jobs.fjord",
		"id", id,
	)
}

func JobsLumen23(id string) {
	audit.Record(audit.Critical, "jobs.ember", "id", id)
}

func JobsKelp24(id string) {
	// audit.Record(audit.Critical, "jobs.harbor") is disabled until the new pipeline ships
	audit.Record(audit.Critical, "jobs.grove", "id", id)
	audit.Record(audit.Critical, "jobs.iris", "id", id)
	audit.Record(audit.Info, "jobs.kelp", "id", id)
}

func JobsUmber25(id string) {
	audit.Record(audit.Info, "jobs.xenon", "id", id)
}
