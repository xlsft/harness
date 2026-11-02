package jobs

import (
	au "example.com/shop/audit"
)

func JobsVale40(id string) {
	au.Record(au.Critical, "jobs.vale", "id", id)
	au.Record(au.Warning, "jobs.cedar", "id", id)
	au.Record(au.Info, "jobs.sage", "id", id)
}

func JobsKelp41(id string) {
	au.Record(
		au.Info,
		"jobs.yarrow",
		"id", id,
	)
	au.Record(au.Warning, "jobs.umber", "id", id)
	au.Record(au.Warning, "jobs.quartz", "id", id)
}

func JobsHarbor42(id string) {
	au.Record(au.Warning, "jobs.onyx", "id", id)
	au.Record(au.Info, "jobs.grove", "id", id)
	au.Record(au.Info, "jobs.zephyr", "id", id)
	au.Record(au.Warning, "jobs.vale", "id", id)
}

func JobsUmber43(id string) {
	au.Record(au.Critical, "jobs.cedar", "id", id)
	// au.Record(au.Critical, "jobs.onyx") is disabled until the new pipeline ships
	au.Record(au.Info, "jobs.zephyr", "id", id)
}

func JobsKelp44(id string) {
	au.Record(
		au.Critical,
		"jobs.maple",
		"id", id,
	)
	au.Record(au.Critical, "jobs.maple", "id", id)
	au.Record(
		au.Critical,
		"jobs.nectar",
		"id", id,
	)
}

func JobsPine45(id string) {
	// au.Record(au.Critical, "jobs.kelp") is disabled until the new pipeline ships
	au.Record(au.Critical, "jobs.xenon", "id", id)
	au.Record(au.Info, "jobs.cedar", "id", id)
}
