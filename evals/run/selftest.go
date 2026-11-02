package main

import (
	"context"
	"fmt"
	"github.com/xlsft/harness/evals/task"
	"os"
	"path/filepath"
	"strings"
)

// selftest validates the tasks without calling any model:
//
//   - check.sh must FAIL on the untouched repo/ snapshot;
//   - when the task ships a reference fix (solution/ overlaid on repo/, and/or
//     solution.txt used as the agent's final answer), check.sh must PASS on it.
func selftest(ctx context.Context, cfg *config, tasks []*task.Task) int {
	tmp, err := os.MkdirTemp(cfg.workRoot, "harness-eval-selftest-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(tmp)

	bad := 0
	fmt.Printf("| Task | Untouched repo | Reference solution |\n|---|---|---|\n")
	for _, t := range tasks {
		before := selfCheck(ctx, cfg, t, tmp, false)
		after := "n/a"
		if hasSolution(t) {
			after = selfCheck(ctx, cfg, t, tmp, true)
		}
		ok := strings.HasPrefix(before, "fails") && (after == "n/a" || strings.HasPrefix(after, "passes"))
		if !ok {
			bad++
		}
		fmt.Printf("| %s | %s | %s |\n", t.ID, before, after)
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "\nselftest: %d task(s) are broken\n", bad)
		return 1
	}
	fmt.Fprintf(os.Stderr, "\nselftest: all %d task(s) OK\n", len(tasks))
	return 0
}

func hasSolution(t *task.Task) bool {
	for _, p := range []string{"solution", "solution.txt"} {
		if _, err := os.Stat(filepath.Join(t.Dir, p)); err == nil {
			return true
		}
	}
	return false
}

func selfCheck(ctx context.Context, cfg *config, t *task.Task, tmp string, withSolution bool) string {
	work, err := prepareWorkdir(t, tmp)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	defer os.RemoveAll(work)
	resultFile := work + ".result.txt"
	defer os.Remove(resultFile)
	var answer []byte
	if withSolution {
		if sol := filepath.Join(t.Dir, "solution"); dirExists(sol) {
			if err := copyTree(sol, work); err != nil {
				return "ERROR: " + err.Error()
			}
		}
		answer, _ = os.ReadFile(filepath.Join(t.Dir, "solution.txt"))
	}
	if err := os.WriteFile(resultFile, answer, 0o644); err != nil {
		return "ERROR: " + err.Error()
	}
	logPath := work + ".check.log"
	defer os.Remove(logPath)
	passed, checkErr := runCheck(ctx, cfg, t, work, resultFile, "selftest", logPath)
	switch {
	case checkErr != "":
		return "ERROR: " + checkErr
	case passed:
		return "passes"
	}
	log, _ := os.ReadFile(logPath)
	return "fails (" + summarize(log) + ")"
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// summarize picks the most telling line of a check log for the table.
func summarize(log []byte) string {
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	pick := ""
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "FAIL:") || strings.HasPrefix(l, "--- FAIL") || strings.Contains(l, "Error") || strings.HasPrefix(l, "✖") {
			pick = l
			break
		}
	}
	if pick == "" && len(lines) > 0 {
		pick = strings.TrimSpace(lines[len(lines)-1])
	}
	pick = strings.ReplaceAll(pick, "|", `\|`)
	if len(pick) > 90 {
		pick = pick[:90] + "..."
	}
	return pick
}
