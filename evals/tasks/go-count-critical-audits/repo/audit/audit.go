package audit

type Level int

const (
	Info Level = iota
	Warning
	Critical
)

// Record writes an audit event.
func Record(level Level, event string, fields ...any) {}
