package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// Limits stop a runaway script. They bound the script itself, not its agents' work: waiting for an agent costs no steps
// and no compute time.
type Limits struct {
	// Steps caps function entries, loop iterations and host calls.
	Steps int64
	// Compute caps the time spent running script code: what stops a builtin looping where no step is counted.
	Compute time.Duration
	// WallClock caps the whole run, waits included.
	WallClock time.Duration
}

// DefaultLimits are generous for a script that only orchestrates, as one should.
var DefaultLimits = Limits{Steps: 10_000_000, Compute: 60 * time.Second, WallClock: 12 * time.Hour}

// maxCallStack bounds recursion: goja's default is unbounded, which ends with the process out of memory.
const maxCallStack = 1024

// allowedGlobals is what a script sees of the global object: pure language features. Nothing reads a clock, a random
// source, a file or the network here, and a global a later goja adds stays out until it is listed.
var allowedGlobals = map[string]bool{
	"Object": true, "Array": true, "String": true, "Number": true, "BigInt": true, "Boolean": true, "RegExp": true, "Symbol": true,
	"Error": true, "AggregateError": true, "TypeError": true, "ReferenceError": true, "SyntaxError": true, "RangeError": true, "EvalError": true, "URIError": true,
	"Math": true, "JSON": true, "Map": true, "Set": true, "WeakMap": true, "WeakSet": true, "Promise": true, "Reflect": true, "Iterator": true,
	"NaN": true, "undefined": true, "Infinity": true, "isNaN": true, "isFinite": true, "parseInt": true, "parseFloat": true,
	"encodeURI": true, "encodeURIComponent": true, "decodeURI": true, "decodeURIComponent": true,
}

// prelude builds the script's API on the host functions, which stay in its closure. parallel and pipeline wait for all
// their work before they throw, so that a failure leaves nothing running behind the script's back. The constructors
// that compile strings go away with the globals: code from a string would not be counted in steps.
const prelude = `(function (host) {
  const P = Promise, parse = JSON.parse, stringify = JSON.stringify, E = Error, S = String, isArray = Array.isArray, from = Array.from;
  delete Math.random;
  for (const f of [function () {}, function* () {}, async function () {}]) {
    Object.defineProperty(Object.getPrototypeOf(f), "constructor", { value: undefined, writable: false, configurable: false });
  }
  const settle = async (work) => {
    const results = await P.allSettled(work);
    for (const r of results) if (r.status === "rejected") throw r.reason;
    return results.map((r) => r.value);
  };
  const start = (f, ...a) => { try { return P.resolve(typeof f === "function" ? f(...a) : f); } catch (e) { return P.reject(e); } };
  return {
    agent: (prompt, options) => host.agent(S(prompt), stringify(options === undefined ? {} : options)),
    parallel: (fns) => {
      if (!isArray(fns)) throw new TypeError("parallel takes an array of functions");
      return settle(fns.map((f) => start(f)));
    },
    pipeline: (items, ...steps) => settle(from(items).map(async (item, index) => {
      let value = item;
      for (const step of steps) {
        if (typeof step !== "function") throw new TypeError("pipeline steps are functions");
        value = await step(value, item, index);
      }
      return value;
    })),
    phase: (title) => host.phase(S(title)),
    log: (...parts) => host.log(parts.map((p) => typeof p === "string" ? p : stringify(p) ?? S(p)).join(" ")),
    parse,
    error: (message, code) => { const e = new E(message); e.code = code; return e; },
    result: (v) => v === undefined ? "" : typeof v === "string" ? v : stringify(v) ?? "",
    reason: (e) => (e && e.code !== undefined) ? S(e.message) : (e && e.stack) ? S(e.stack) : S(e),
  };
})`

// agentCall is one agent() call.
type agentCall struct {
	Prompt string
	Options
}

// Options are the options of agent(). Label and Phase only name the call; the others make it another call.
type Options struct {
	Label     string          `json:"label,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	Schema    json.RawMessage `json:"schema,omitempty"`
	Model     string          `json:"model,omitempty"`
	Type      string          `json:"type,omitempty"`
	Isolation string          `json:"isolation,omitempty"`
}

// outcome is how an agent() call ended.
type outcome struct {
	// Text or Output (with a schema) is the value; Err rejects the call, with Code on the error.
	Text   string
	Output json.RawMessage
	Err    string
	Code   string
}

// scriptHost is what a script reaches the outside through. Its methods are called on the engine's goroutine, and agent
// must not block: done may be called from anywhere, once.
type scriptHost interface {
	agent(call agentCall, done func(outcome))
	phase(title string)
	log(line string)
	// limitHit says the compute limit was reached. The script ends with err at its next instruction; one stuck in a
	// builtin has none coming, and the host may give up on it.
	limitHit(err error)
}

// runError is a limit or a stop ending a run; the script cannot catch it.
type runError struct{ msg string }

func (e *runError) Error() string { return e.msg }

// engine is one execution of a script: an interpreter that only this goroutine touches, and an event loop that feeds it
// what its agents come back with.
type engine struct {
	vm   *goja.Runtime
	host scriptHost
	lim  Limits

	steps int64
	spent time.Duration
	// waiting counts agent() calls that have not come back; api is the prelude's result.
	waiting int
	api     *goja.Object

	mu     sync.Mutex
	queue  []func() error
	signal chan struct{}
}

// execute runs a script to its end, and returns what it returned, as text. ctx stops it.
func execute(ctx context.Context, s *script, args json.RawMessage, host scriptHost, lim Limits) (string, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, lim.WallClock, &runError{fmt.Sprintf("the workflow hit its wall-clock limit of %s", lim.WallClock)})
	defer cancel()
	e := &engine{vm: goja.New(), host: host, lim: lim, signal: make(chan struct{}, 1)}
	e.vm.SetMaxCallStackSize(maxCallStack)
	stopVM := context.AfterFunc(ctx, func() { e.vm.Interrupt(context.Cause(ctx)) })
	defer stopVM()

	var promise *goja.Promise
	if err := e.slice(func() error {
		var err error
		promise, err = e.start(s, args)
		return err
	}); err != nil {
		return "", e.failure(ctx, err)
	}
	for promise.State() == goja.PromiseStatePending {
		e.mu.Lock()
		var next func() error
		if len(e.queue) > 0 {
			next, e.queue = e.queue[0], e.queue[1:]
		}
		e.mu.Unlock()
		if next != nil {
			e.waiting--
			if err := e.slice(next); err != nil {
				return "", e.failure(ctx, err)
			}
			continue
		}
		if e.waiting == 0 {
			return "", errors.New("the script is waiting on a promise that nothing will settle")
		}
		select {
		case <-e.signal:
		case <-ctx.Done():
			return "", e.failure(ctx, context.Cause(ctx))
		}
	}
	var out string
	err := e.slice(func() error {
		fn := "result"
		if promise.State() == goja.PromiseStateRejected {
			fn = "reason"
		}
		call, _ := goja.AssertFunction(e.api.Get(fn))
		v, err := call(goja.Undefined(), promise.Result())
		if err == nil {
			out = v.String()
		}
		return err
	})
	if err != nil {
		return "", e.failure(ctx, err)
	}
	if promise.State() == goja.PromiseStateRejected {
		return "", errors.New(out)
	}
	return out, nil
}

// start hardens the interpreter, and calls the script.
func (e *engine) start(s *script, rawArgs json.RawMessage) (*goja.Promise, error) {
	vm := e.vm
	v, err := vm.RunString(prelude)
	if err != nil {
		return nil, err
	}
	build, _ := goja.AssertFunction(v)
	hostObj := vm.NewObject()
	_ = hostObj.Set("agent", e.agent)
	_ = hostObj.Set("phase", func(c goja.FunctionCall) goja.Value {
		e.tick()
		e.host.phase(c.Argument(0).String())
		return goja.Undefined()
	})
	_ = hostObj.Set("log", func(c goja.FunctionCall) goja.Value {
		e.tick()
		e.host.log(c.Argument(0).String())
		return goja.Undefined()
	})
	built, err := build(goja.Undefined(), hostObj)
	if err != nil {
		return nil, err
	}
	e.api = built.ToObject(vm)
	global := vm.GlobalObject()
	for _, name := range global.GetOwnPropertyNames() {
		if !allowedGlobals[name] {
			_ = global.Delete(name)
		}
	}
	wrapper, err := vm.RunProgram(s.prog)
	if err != nil {
		return nil, err
	}
	fn, ok := goja.AssertFunction(wrapper)
	if !ok {
		return nil, errors.New("the script does not compile to a function")
	}
	if len(rawArgs) == 0 {
		rawArgs = json.RawMessage("null")
	}
	parse, _ := goja.AssertFunction(e.api.Get("parse"))
	jsArgs, err := parse(goja.Undefined(), vm.ToValue(string(rawArgs)))
	if err != nil {
		return nil, fmt.Errorf("args are not JSON: %w", err)
	}
	r, err := fn(goja.Undefined(), jsArgs, e.api.Get("agent"), e.api.Get("parallel"), e.api.Get("pipeline"), e.api.Get("phase"), e.api.Get("log"), vm.ToValue(e.tickFn))
	if err != nil {
		return nil, err
	}
	p, ok := r.Export().(*goja.Promise)
	if !ok {
		return nil, errors.New("the script did not start")
	}
	return p, nil
}

func (e *engine) tickFn(goja.FunctionCall) goja.Value {
	e.tick()
	return goja.Undefined()
}

// tick counts a step. Over the cap it interrupts, which a script's try/catch does not see.
func (e *engine) tick() {
	e.steps++
	if e.steps == e.lim.Steps+1 {
		e.vm.Interrupt(&runError{fmt.Sprintf("the workflow's script hit its cap of %d steps", e.lim.Steps)})
	}
}

// slice runs script code, charged to the compute limit.
func (e *engine) slice(f func() error) error {
	left := e.lim.Compute - e.spent
	if left <= 0 {
		left = time.Millisecond
	}
	timer := time.AfterFunc(left, func() {
		err := &runError{fmt.Sprintf("the workflow's script used up its limit of %s of running time (waiting for agents does not count)", e.lim.Compute)}
		e.vm.Interrupt(err)
		e.host.limitHit(err)
	})
	began := time.Now()
	err := f()
	timer.Stop()
	e.spent += time.Since(began)
	return err
}

// failure makes an error from the interpreter one to report.
func (e *engine) failure(ctx context.Context, err error) error {
	var in *goja.InterruptedError
	if errors.As(err, &in) {
		if inner, ok := in.Value().(error); ok {
			return inner
		}
		return errors.New(in.String())
	}
	if c := context.Cause(ctx); c != nil && ctx.Err() != nil {
		return c
	}
	return err
}

// post hands the loop work to do on its goroutine, in order.
func (e *engine) post(f func() error) {
	e.mu.Lock()
	e.queue = append(e.queue, f)
	e.mu.Unlock()
	select {
	case e.signal <- struct{}{}:
	default:
	}
}

// agent is the host side of agent(): always a promise, also for a call that is refused at once.
func (e *engine) agent(c goja.FunctionCall) goja.Value {
	e.tick()
	p, resolve, reject := e.vm.NewPromise()
	settle := func(o outcome) {
		e.post(func() error { return e.settle(o, resolve, reject) })
	}
	e.waiting++
	call := agentCall{Prompt: c.Argument(0).String()}
	if strings.TrimSpace(call.Prompt) == "" {
		settle(outcome{Err: "agent() needs a prompt", Code: "invalid"})
		return e.vm.ToValue(p)
	}
	if problem := decodeOptions(c.Argument(1).String(), &call.Options); problem != "" {
		settle(outcome{Err: problem, Code: "invalid"})
		return e.vm.ToValue(p)
	}
	e.host.agent(call, settle)
	return e.vm.ToValue(p)
}

// settle gives a call's outcome to the script.
func (e *engine) settle(o outcome, resolve, reject func(any) error) error {
	callOf := func(name string) goja.Callable {
		f, _ := goja.AssertFunction(e.api.Get(name))
		return f
	}
	if o.Err != "" {
		v, err := callOf("error")(goja.Undefined(), e.vm.ToValue(o.Err), e.vm.ToValue(o.Code))
		if err != nil {
			return err
		}
		return reject(v)
	}
	if len(o.Output) > 0 {
		v, err := callOf("parse")(goja.Undefined(), e.vm.ToValue(string(o.Output)))
		if err != nil {
			return err
		}
		return resolve(v)
	}
	return resolve(e.vm.ToValue(o.Text))
}

// decodeOptions reads the options of agent(), refusing what is not one: a misspelt option should not pass unnoticed.
func decodeOptions(raw string, o *Options) string {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(o); err != nil {
		return "agent() options: label, phase, model, type and isolation are strings, schema is a JSON schema: " + err.Error()
	}
	if o.Isolation != "" && o.Isolation != "worktree" {
		return `agent() options: isolation must be "worktree"`
	}
	return ""
}
