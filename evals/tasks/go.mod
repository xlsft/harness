// This stub module keeps the task fixtures (hidden tests, reference solutions,
// repo snapshots) out of the evals module, so `go vet ./...` and
// `go build ./...` in evals/ only see the runner. It is never built.
module github.com/xlsft/harness/evals/tasks

go 1.22
