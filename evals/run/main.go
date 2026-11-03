// Command run executes the harness eval suite: every selected model runs every
// selected task through the headless harness client, and each attempt is graded
// by the task's deterministic check.sh.
//
//	go run ./run -models claude-sonnet-4-6,deepseek-chat -parallel 4
//	go run ./run -selftest            # validate tasks without any model
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/xlsft/harness/evals/task"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	models       []string
	tasks        []string
	parallel     int
	repeat       int
	client       string
	server       string
	extraArgs    []string
	tasksDir     string
	outDir       string
	workRoot     string
	keep         bool
	checkTimeout time.Duration
	selftest     bool
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, " ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func defaultTasksDir() string {
	for _, d := range []string{"tasks", filepath.Join("evals", "tasks")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return "tasks"
}

func parseFlags() *config {
	cfg := &config{}
	models := flag.String("models", os.Getenv("HARNESS_EVAL_MODELS"), "comma-separated model ids from the server's model catalog (env HARNESS_EVAL_MODELS)")
	tasks := flag.String("tasks", "", "comma-separated task ids (default: all)")
	flag.IntVar(&cfg.parallel, "parallel", 1, "number of runs executed concurrently")
	flag.IntVar(&cfg.repeat, "repeat", 1, "attempts per model x task")
	flag.StringVar(&cfg.client, "client", envOr("HARNESS_CLIENT", "harness-client"), "path to the harness-client binary (env HARNESS_CLIENT)")
	flag.StringVar(&cfg.server, "server", os.Getenv("HARNESS_SERVER"), "harness server URL passed as --server (env HARNESS_SERVER; default: stored credentials)")
	flag.Var((*stringList)(&cfg.extraArgs), "client-arg", "extra argument appended to every client invocation (repeatable)")
	flag.StringVar(&cfg.tasksDir, "tasks-dir", defaultTasksDir(), "directory containing the tasks")
	flag.StringVar(&cfg.outDir, "out", "", "output directory (default: results/<timestamp>)")
	flag.StringVar(&cfg.workRoot, "workdir", "", "parent directory for per-run working copies (default: system temp dir)")
	flag.BoolVar(&cfg.keep, "keep", false, "keep per-run working copies after grading")
	flag.DurationVar(&cfg.checkTimeout, "check-timeout", 2*time.Minute, "timeout for each check.sh run")
	flag.BoolVar(&cfg.selftest, "selftest", false, "validate tasks: check.sh must fail on the untouched repo and pass with the reference solution; no model is called")
	flag.Parse()
	cfg.models, cfg.tasks = splitList(*models), splitList(*tasks)
	if cfg.parallel < 1 {
		cfg.parallel = 1
	}
	if cfg.repeat < 1 {
		cfg.repeat = 1
	}
	if cfg.outDir == "" {
		cfg.outDir = filepath.Join("results", time.Now().UTC().Format("20060102-150405"))
	}
	return cfg
}

func main() {
	cfg := parseFlags()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tasks, err := task.LoadAll(cfg.tasksDir, cfg.tasks)
	if err != nil {
		fatalf("loading tasks: %v", err)
	}
	if len(tasks) == 0 {
		fatalf("no tasks found in %s", cfg.tasksDir)
	}
	if cfg.selftest {
		os.Exit(selftest(ctx, cfg, tasks))
	}
	if len(cfg.models) == 0 {
		fatalf("no models given: pass -models a,b or set HARNESS_EVAL_MODELS")
	}
	if p, err := exec.LookPath(cfg.client); err != nil {
		fatalf("harness client %q not found: %v (use -client or HARNESS_CLIENT)", cfg.client, err)
	} else if cfg.client, err = filepath.Abs(p); err != nil {
		fatalf("%v", err)
	}
	if cfg.outDir, err = filepath.Abs(cfg.outDir); err != nil {
		fatalf("%v", err)
	}
	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		fatalf("%v", err)
	}

	type job struct {
		model  string
		task   *task.Task
		repeat int
	}
	var jobs []job
	for _, m := range cfg.models {
		for _, t := range tasks {
			for r := 1; r <= cfg.repeat; r++ {
				jobs = append(jobs, job{m, t, r})
			}
		}
	}
	fmt.Fprintf(os.Stderr, "running %d attempts (%d models x %d tasks x %d repeats), parallel %d\n",
		len(jobs), len(cfg.models), len(tasks), cfg.repeat, cfg.parallel)

	started := time.Now()
	results := make([]RunResult, len(jobs))
	ch := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for w := 0; w < cfg.parallel; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				j := jobs[i]
				res := runOne(ctx, cfg, j.model, j.task, j.repeat)
				results[i] = res
				mu.Lock()
				done++
				fmt.Fprintf(os.Stderr, "[%d/%d] %-6s %s / %s #%d (%d turns, %s)%s\n", done, len(jobs),
					strings.ToUpper(res.Status), res.Model, res.Task, res.Repeat, res.Turns,
					time.Duration(res.DurationMS)*time.Millisecond, errSuffix(res.Error))
				mu.Unlock()
			}
		}()
	}
	for i := range jobs {
		if ctx.Err() != nil {
			break
		}
		ch <- i
	}
	close(ch)
	wg.Wait()

	var finished []RunResult
	for _, r := range results {
		if r.Model != "" {
			finished = append(finished, r)
		}
	}
	rep := buildReport(cfg, tasks, finished, started)
	if err := writeReport(cfg.outDir, rep); err != nil {
		fatalf("writing report: %v", err)
	}
	fmt.Print(rep.Markdown())
	fmt.Fprintf(os.Stderr, "\nresults: %s\n", cfg.outDir)
	if ctx.Err() != nil {
		os.Exit(130)
	}
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	if len(e) > 120 {
		e = e[:120] + "..."
	}
	return " " + e
}

var unsafePath = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string { return unsafePath.ReplaceAllString(s, "_") }

// runOne performs a single attempt: prepare a working copy, run the agent, grade it.
func runOne(ctx context.Context, cfg *config, model string, t *task.Task, repeat int) RunResult {
	res := RunResult{Model: model, Task: t.ID, Repeat: repeat, Status: StatusError}
	logDir := filepath.Join(cfg.outDir, "runs", safeName(model), t.ID, fmt.Sprintf("%d", repeat))
	res.LogDir = logDir
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		res.Error = err.Error()
		return res
	}
	work, err := prepareWorkdir(t, cfg.workRoot)
	if work != "" && !cfg.keep {
		defer os.RemoveAll(work)
	}
	if err != nil {
		res.Error = "prepare: " + err.Error()
		return res
	}
	if cfg.keep {
		res.Workdir = work
	}

	args := clientArgs(cfg, model, t)
	writeFile(filepath.Join(logDir, "command.txt"), []byte(shellQuote(append([]string{cfg.client}, args...))+"\n"))
	// Eval sessions are throwaway: keep them off the server's session list.
	p := runProc(ctx, time.Duration(t.Timeout), work, []string{"HARNESS_NO_SYNC=1"}, cfg.client, args...)
	writeFile(filepath.Join(logDir, "client.stdout"), p.stdout)
	writeFile(filepath.Join(logDir, "client.stderr"), p.stderr)
	res.DurationMS = p.duration.Milliseconds()
	res.ClientExit = p.exitCode
	res.TimedOut = p.timedOut

	resultText := ""
	if cr, ok := parseClientOutput(p.stdout); ok {
		res.Parsed = true
		res.Turns, res.Usage, res.CostUSD = cr.NumTurns, cr.Usage, cr.cost()
		res.Subtype, res.IsError = cr.Subtype, cr.IsError
		resultText = cr.Result
		if cr.IsError && len(cr.Errors) > 0 {
			res.Error = strings.Join(cr.Errors, "; ")
		}
	}
	resultFile := filepath.Join(logDir, "result.txt")
	writeFile(resultFile, []byte(resultText))
	switch {
	case p.err != nil:
		res.Error = "client: " + p.err.Error()
		return res
	case p.timedOut:
		res.Status = StatusTimeout
		res.Error = fmt.Sprintf("timed out after %s", time.Duration(t.Timeout))
		return res
	case !res.Parsed && res.Error == "":
		res.Error = fmt.Sprintf("could not parse client JSON output (exit %d): %s", p.exitCode, lastLine(p.stderr))
	}

	// Grade even when the client reported an error: the agent may have finished
	// the work before hitting max_turns.
	passed, checkErr := runCheck(ctx, cfg, t, work, resultFile, model, filepath.Join(logDir, "check.log"))
	res.Passed = passed
	switch {
	case passed:
		res.Status = StatusPass
	case checkErr != "":
		res.Status, res.Error = StatusError, checkErr
	default:
		res.Status = StatusFail
	}
	return res
}

// runCheck runs check.sh in the working copy. It returns whether the check
// passed and, if it could not be run at all, why.
func runCheck(ctx context.Context, cfg *config, t *task.Task, work, resultFile, model, logPath string) (bool, string) {
	env := []string{
		"EVAL_TASK_DIR=" + t.Dir,
		"EVAL_WORKDIR=" + work,
		"EVAL_RESULT_FILE=" + resultFile,
		"EVAL_MODEL=" + model,
	}
	p := runProc(ctx, cfg.checkTimeout, work, env, "bash", filepath.Join(t.Dir, "check.sh"))
	log := append(append([]byte{}, p.stdout...), p.stderr...)
	writeFile(logPath, log)
	switch {
	case p.err != nil:
		return false, "check: " + p.err.Error()
	case p.timedOut:
		return false, fmt.Sprintf("check timed out after %s", cfg.checkTimeout)
	}
	return p.exitCode == 0, ""
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

func shellQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~!") {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "eval: "+format+"\n", args...)
	os.Exit(2)
}
