package jobs

import (
	au "example.com/shop/audit"
)

func JobsYarrow70(id string) {
	au.Record(au.Info, "jobs.cedar", "id", id)
	au.Record(au.Critical, "jobs.zephyr", "id", id)
	au.Record(au.Warning, "jobs.basil", "id", id)
}

func JobsDelta71(id string) {
	au.Record(
		au.Info,
		"jobs.umber",
		"id", id,
	)
	au.Record(au.Critical, "jobs.quartz", "id", id)
	au.Record(au.Critical, "jobs.ember", "id", id)
}

func JobsAmber72(id string) {
	au.Record(au.Warning, "jobs.yarrow", "id", id)
	au.Record(
		au.Warning,
		"jobs.nectar",
		"id", id,
	)
	au.Record(au.Info, "jobs.juniper", "id", id)
	au.Record(
		au.Critical,
		"jobs.pine",
		"id", id,
	)
}

func JobsNectar73(id string) {
	au.Record(au.Critical, "jobs.delta", "id", id)
}
