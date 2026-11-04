package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHost answers agent() calls with a function, on another goroutine as real agents would.
type fakeHost struct {
	mu     sync.Mutex
	calls  []agentCall
	phases []string
	logs   []string
	answer func(agentCall) outcome
}

func (h *fakeHost) agent(c agentCall, done func(outcome)) {
	h.mu.Lock()
	h.calls = append(h.calls, c)
	h.mu.Unlock()
	go func() {
		if h.answer == nil {
			done(outcome{Text: "reply to " + c.Prompt})
			return
		}
		done(h.answer(c))
	}()
}
func (h *fakeHost) phase(t string)     { h.mu.Lock(); h.phases = append(h.phases, t); h.mu.Unlock() }
func (h *fakeHost) log(l string)       { h.mu.Lock(); h.logs = append(h.logs, l); h.mu.Unlock() }
func (h *fakeHost) limitHit(err error) {}

const header = `export const meta = { name: "t", description: "d", phases: ["a", "b"] }
`

var testLimits = Limits{Steps: 1_000_000, Compute: 5 * time.Second, WallClock: 30 * time.Second}

func runWith(t *testing.T, h *fakeHost, lim Limits, body string, args string) (string, error) {
	t.Helper()
	s, err := parseScript(header + body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return execute(context.Background(), s, json.RawMessage(args), h, lim)
}

func mustRun(t *testing.T, body string) string {
	t.Helper()
	out, err := runWith(t, &fakeHost{}, testLimits, body, "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out
}

func TestReturnValuesAndArgs(t *testing.T) {
	for _, tc := range []struct{ body, args, want string }{
		{`return "plain"`, "", "plain"},
		{`return { a: [1, 2], b: args.x + 1 }`, `{"x": 41}`, `{"a":[1,2],"b":42}`},
		{``, "", ""},
		{`return await agent("hi")`, "", "reply to hi"},
		{`const log = 1; const args = 2; return log + args`, "", "3"},
	} {
		s, err := parseScript(header + tc.body)
		if err != nil {
			t.Fatal(err)
		}
		got, err := execute(context.Background(), s, json.RawMessage(tc.args), &fakeHost{}, testLimits)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q, %v", tc.body, got, err)
		}
	}
}

// parallel runs at once and keeps the order; a pipeline's items go at their own pace, each step starting when the one
// before it ended for that item.
func TestParallelAndPipeline(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	started := map[string]bool{}
	h := &fakeHost{answer: func(c agentCall) outcome {
		mu.Lock()
		started[c.Prompt] = true
		mu.Unlock()
		if c.Prompt == "slow:1" {
			<-release
		}
		return outcome{Text: c.Prompt}
	}}
	go func() {
		// The fast item goes through both its steps while the slow item is still in its first.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := started["next:fast:2"]
			mu.Unlock()
			if ok {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(release)
	}()
	out, err := runWith(t, h, testLimits, `
		const p = await parallel([() => agent("x"), () => agent("y")])
		const q = await pipeline([1, 2], (i) => agent(i === 1 ? "slow:1" : "fast:2"), (prev, item, index) => agent("next:" + prev).then((v) => v + "/" + item + "/" + index))
		return [p, q]`, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := `[["x","y"],["next:slow:1/1/0","next:fast:2/2/1"]]`; out != want {
		t.Fatalf("got %s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if !started["next:slow:1"] {
		t.Fatal("the slow item never got to its next step")
	}
}

// A failure rejects after all the work has ended; the script can catch it.
func TestFailuresAreThrown(t *testing.T) {
	var mu sync.Mutex
	finished := 0
	h := &fakeHost{answer: func(c agentCall) outcome {
		if c.Prompt == "bad" {
			return outcome{Err: "no luck", Code: "failed"}
		}
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		finished++
		mu.Unlock()
		return outcome{Text: "ok"}
	}}
	out, err := runWith(t, h, testLimits, `
		try { await parallel([() => agent("bad"), () => agent("good")]) } catch (e) { return e.code + ":" + e.message }
		return "not thrown"`, "")
	if err != nil || out != "failed:no luck" {
		t.Fatalf("%q, %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if finished != 1 {
		t.Fatal("parallel threw before all its work ended")
	}
	if _, err := runWith(t, h, testLimits, `await agent("bad")`, ""); err == nil || !strings.Contains(err.Error(), "no luck") {
		t.Fatalf("uncaught failure: %v", err)
	}
	if _, err := runWith(t, h, testLimits, `throw new Error("mine")`, ""); err == nil || !strings.Contains(err.Error(), "mine") {
		t.Fatalf("throw: %v", err)
	}
}

func TestAgentOptions(t *testing.T) {
	h := &fakeHost{answer: func(agentCall) outcome { return outcome{Output: json.RawMessage(`{"n":2}`)} }}
	out, err := runWith(t, h, testLimits, `
		const v = await agent("p", { label: "l", phase: "a", model: "m", type: "Explore", isolation: "worktree", schema: { type: "object" } })
		return v.n + 1`, "")
	if err != nil || out != "3" {
		t.Fatalf("%q, %v", out, err)
	}
	c := h.calls[0]
	if c.Label != "l" || c.Phase != "a" || c.Model != "m" || c.Type != "Explore" || c.Isolation != "worktree" || string(c.Schema) != `{"type":"object"}` {
		t.Fatalf("%+v", c)
	}
	for _, body := range []string{`await agent("p", { lable: "typo" })`, `await agent("p", { isolation: "vm" })`, `await agent("")`, `await agent("p", { label: 3 })`} {
		if _, err := runWith(t, &fakeHost{}, testLimits, body, ""); err == nil {
			t.Fatalf("%s passed", body)
		}
	}
}

func TestPhaseAndLog(t *testing.T) {
	h := &fakeHost{}
	if _, err := runWith(t, h, testLimits, `phase("a"); log("one", 2, {x: 1}); phase("b")`, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.phases, ",") != "a,b" || strings.Join(h.logs, ",") != `one 2 {"x":1}` {
		t.Fatalf("%v %v", h.phases, h.logs)
	}
}

// What a script can reach of the host, and of what would make a resume replay differently: nothing.
func TestSandbox(t *testing.T) {
	for _, name := range []string{"Date", "eval", "Function", "globalThis", "require", "process", "module", "console", "setTimeout", "setInterval", "queueMicrotask",
		"fetch", "XMLHttpRequest", "WebSocket", "Proxy", "ArrayBuffer", "GoError", "host"} {
		if out := mustRun(t, `return typeof `+name); out != "undefined" {
			t.Errorf("%s is %s", name, out)
		}
	}
	for _, tc := range []struct{ body, want string }{
		{`return typeof Math.random`, "undefined"},
		{`return Object.getOwnPropertyNames(this === undefined ? {} : this).length`, "0"},
		{`return (function () { return this === undefined })()`, "true"},
		{`return String((() => {}).constructor)`, "undefined"},
		{`return String((function () {}).constructor)`, "undefined"},
		{`return String((async () => {}).constructor)`, "undefined"},
		{`return String((function* () {}).constructor)`, "undefined"},
		{`return String(agent.constructor)`, "undefined"},
	} {
		if out := mustRun(t, tc.body); out != tc.want {
			t.Errorf("%s: %q", tc.body, out)
		}
	}
	// Forms that would reach out fail outright.
	for _, body := range []string{
		`return Function("return this")()`,
		`return (() => {}).constructor("return this")()`,
		`return new Date()`,
		`return Date.now()`,
		`return Math.random()`,
		`return require("fs")`,
		`return await import("fs")`,
		`setTimeout(() => {}, 1)`,
	} {
		if s, err := parseScript(header + body); err == nil {
			if out, err := execute(context.Background(), s, nil, &fakeHost{}, testLimits); err == nil {
				t.Errorf("%s gave %q", body, out)
			}
		}
	}
}

// A sourceMappingURL comment would make goja's default loader read that file.
func TestSourceMapCommentReadsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "map.json")
	if err := os.WriteFile(path, []byte(`{not a map`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, "return 1\n//# sourceMappingURL="+path); out != "1" {
		t.Fatal(out)
	}
}

func TestStepCap(t *testing.T) {
	lim := Limits{Steps: 2000, Compute: 30 * time.Second, WallClock: 30 * time.Second}
	for _, body := range []string{
		`while (true) {}`,
		`for (;;);`,
		`for (;;) (Math.abs)(1)`,
		`do { Math.abs(1) } while (true)`,
		`let i = 0; do i++; while (true)`,
		`while (true) { try { while (true) {} } catch (e) {} }`,
		`const f = function () { return 1 }; for (let i = 0; i < 1e9; i++) if (i < 0) f(); else f()`,
		`for (const x of (function* () { for (;;) yield 1 })()) {}`,
		`await parallel([async () => { for (;;) {} }])`,
		`await agent("x"); for (;;) {}`,
	} {
		out, err := runWith(t, &fakeHost{}, lim, body, "")
		if err == nil || !strings.Contains(err.Error(), "cap of 2000 steps") {
			t.Errorf("%s: %q, %v", body, out, err)
		}
	}
	// Under the cap nothing shows.
	if out, err := runWith(t, &fakeHost{}, lim, `let n = 0; for (let i = 0; i < 100; i++) n += i; return n`, ""); err != nil || out != "4950" {
		t.Fatalf("%q, %v", out, err)
	}
}

// The cap counts what the script does, so it does not depend on the machine: the same script stops at the same place.
func TestStepCapIsExact(t *testing.T) {
	stopAt := func() (int, error) {
		h := &fakeHost{}
		_, err := runWith(t, h, Limits{Steps: 100, Compute: 30 * time.Second, WallClock: 30 * time.Second}, `for (let i = 0; ; i++) log(String(i))`, "")
		n := len(h.logs)
		return n, err
	}
	a, err := stopAt()
	if err == nil {
		t.Fatal("no cap")
	}
	if b, _ := stopAt(); a != b || a == 0 {
		t.Fatalf("%d then %d", a, b)
	}
}

// busyLoop runs long in no statement that counts a step, without allocating.
const busyLoop = `const a = Array.from({ length: 2000 }, (_, i) => i); return a.reduce((s, x) => s + a.reduce((t, y) => t + a.reduce((u, z) => u + z, 0), 0), 0)`

func TestComputeLimit(t *testing.T) {
	// No step is counted in an arrow with a bare expression, called from a builtin.
	lim := Limits{Steps: 1_000_000, Compute: 150 * time.Millisecond, WallClock: 30 * time.Second}
	began := time.Now()
	_, err := runWith(t, &fakeHost{}, lim, busyLoop, "")
	if err == nil {
		t.Fatal("no limit")
	}
	if time.Since(began) > 25*time.Second {
		t.Fatalf("took %s", time.Since(began))
	}
}

func TestRecursionHitsTheStackLimit(t *testing.T) {
	_, err := runWith(t, &fakeHost{}, Limits{Steps: 100_000_000, Compute: 20 * time.Second, WallClock: 30 * time.Second}, `const f = (n) => f(n + 1); return f(0)`, "")
	if err == nil {
		t.Fatal("unbounded recursion passed")
	}
}

func TestWallClockAndStuckScripts(t *testing.T) {
	never := make(chan struct{})
	defer close(never)
	h := &fakeHost{answer: func(agentCall) outcome { <-never; return outcome{} }}
	_, err := runWith(t, h, Limits{Steps: 1000, Compute: time.Second, WallClock: 100 * time.Millisecond}, `await agent("x")`, "")
	if err == nil || !strings.Contains(err.Error(), "wall-clock") {
		t.Fatalf("%v", err)
	}
	// A promise nothing can settle cannot wait until the wall clock.
	began := time.Now()
	_, err = runWith(t, &fakeHost{}, testLimits, `await new Promise(() => {})`, "")
	if err == nil || !strings.Contains(err.Error(), "nothing will settle") || time.Since(began) > 5*time.Second {
		t.Fatalf("%v after %s", err, time.Since(began))
	}
}

func TestCancelStopsARunawayScript(t *testing.T) {
	s, err := parseScript(header + busyLoop)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := execute(ctx, s, nil, &fakeHost{}, Limits{Steps: 1 << 40, Compute: time.Hour, WallClock: time.Hour})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("did not stop")
	}
}

// The same script gives the same result, however the agents' answers arrive.
func TestDeterminism(t *testing.T) {
	body := `
		const a = await parallel([1, 2, 3].map((i) => () => agent("p" + i)))
		const m = new Map(); a.forEach((v, i) => m.set(v, i))
		const o = {}; for (const [k, v] of m) o[k] = v
		return { keys: Object.keys(o), hash: JSON.stringify(o).length, sym: String(Symbol("s")), err: new Error("x").message }`
	var first string
	for i := range 5 {
		h := &fakeHost{answer: func(c agentCall) outcome {
			time.Sleep(time.Duration((i*7+len(c.Prompt)*13+int(c.Prompt[1]))%5) * time.Millisecond)
			return outcome{Text: c.Prompt}
		}}
		out, err := runWith(t, h, testLimits, body, "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = out
		} else if out != first {
			t.Fatalf("%s then %s", first, out)
		}
	}
}

func TestMetaIsReadWithoutRunning(t *testing.T) {
	s, err := parseScript(`// a comment
/* and another */
export const meta = {
  name: "n",
  'description': "d é",
  phases: ["one", "two"],
  extra: { nested: [1, true, null] },
}
throw new Error("never reached")`)
	if err != nil {
		t.Fatal(err)
	}
	if s.meta.Name != "n" || s.meta.Description != "d é" || strings.Join(s.meta.Phases, ",") != "one,two" {
		t.Fatalf("%+v", s.meta)
	}
	for _, bad := range []string{
		``,
		`const meta = { name: "n", description: "d" }`,
		`const x = 1; export const meta = { name: "n", description: "d" }`,
		`export const meta = compute()`,
		`export const meta = { name: agent("x"), description: "d" }`,
		`export const meta = { name: "a" + "b", description: "d" }`,
		"export const meta = { name: `a${1}`, description: \"d\" }",
		`export const meta = { ["name"]: "n", description: "d" }`,
		`export const meta = { get name() { return "n" }, description: "d" }`,
		`export const meta = { ...{ name: "n" }, description: "d" }`,
		`export const meta = { name: "n" }`,
		`export const meta = { name: 1, description: "d" }`,
		`export const meta = { name: "n", description: "d", phases: "one" }`,
		`export const meta = { name: "n", description: "d" }; export const y = 1`,
		`export const meta = { name: "n", description: "d" }; ((`,
		`export const meta = { name: "n", description: "d" }; $$tick()`,
	} {
		if _, err := parseScript(bad); err == nil {
			t.Errorf("%s passed", bad)
		}
	}
}

// The counting must not change what code does: statements around a loop, else branches, and offsets past non-ASCII text.
func TestInstrumentationKeepsMeaning(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`let n = 0; if (false) for (;;) n++; else n = 7; return n`, "7"},
		{`let n = 0; if (true) while (n < 3) n++; else n = 7; return n`, "3"},
		{`let n = 0; do n++; while (n < 3); return n`, "3"},
		{`let n = 0; for (const x of [1, 2]) n += x; return n`, "3"},
		{`let n = 0; for (const k in { a: 1, b: 2 }) n++; return n`, "2"},
		{`let n = 0; lbl: for (;;) { for (;;) { n++; break lbl } } return n`, "1"},
		{`let n = 0; for (let i = 0; i < 3; i++) (n += i); return n`, "3"},
		{`let n = 0; while (n < 2) (function () { n++ })(); return n`, "2"},
		{`let n = 0; while (n < 2) { n++; continue; } return n`, "2"},
		{"const s = \"héllo wörld ✓ 𝒳\"; let n = 0; for (const c of s) n++; /* ✓ */ while (n > 0) n--; return s.length + n", "16"},
		{`class A { constructor() { this.v = 1 } get g() { return this.v + 1 } static s() { return 3 } } class B extends A { constructor() { super(); this.w = 2 } m() { return this.v + this.w } } return new B().m() + new A().g + A.s()`, "8"},
		{`const o = { f() { return 1 }, g: function () { return 2 }, h: () => { return 3 }, i: async () => { return 4 } }; return o.f() + o.g() + o.h() + await o.i()`, "10"},
		{`function* g() { yield 1; yield 2 } return [...g()].length`, "2"},
		{`async function a() { return 1 } return await a()`, "1"},
		{`return ((a, b = 2) => { return a + b })(1)`, "3"},
	} {
		if out := mustRun(t, tc.body); out != tc.want {
			t.Errorf("%s: %q", tc.body, out)
		}
	}
	// The step counting really was compiled in, not the fallback.
	s, err := parseScript(header + `while (true) (Math.abs)(1)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background(), s, nil, &fakeHost{}, Limits{Steps: 10, Compute: 30 * time.Second, WallClock: 30 * time.Second}); err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("%v", err)
	}
}

func ExampleOptions() {
	var o Options
	fmt.Println(decodeOptions(`{"label":"x"}`, &o) == "", o.Label)
	// Output: true x
}
