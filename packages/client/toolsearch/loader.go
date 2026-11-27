package toolsearch

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/xlsft/harness/packages/client/llm"
)

// Name is the tool that loads deferred tools; its description and schema come from the prompt bundle.
const Name = "ToolSearch"

// ReminderDeferredTools is the reminder that lists the deferred tools ({{tools}}: one "name: summary" a line).
const ReminderDeferredTools = "deferred_tools"

// maxSummary caps the one-line summary of a deferred tool in the listing.
const maxSummary = 160

// Source supplies the deferred tools of one kind (built-in, MCP).
type Source interface {
	// DeferredDefs lists the tools of the source that are deferred and could be offered now (disabled ones left
	// out), in a stable order.
	DeferredDefs() []llm.ToolDef
}

// SourceFunc adapts a function to Source.
type SourceFunc func() []llm.ToolDef

// DeferredDefs implements Source.
func (f SourceFunc) DeferredDefs() []llm.ToolDef { return f() }

// Loader is the session's set of deferred tools and of the ones the model loaded. It is shared by the session's
// agents, like the MCP catalog: a tool loaded once stays loaded for the session, through compactions, and a resumed
// session loads again what its transcript says it loaded. The zero value is not usable; a nil *Loader defers nothing.
type Loader struct {
	builtin bool

	mu      sync.Mutex
	sources []Source
	loaded  map[string]bool
	order   []string
	onLoad  func(names []string)
}

// NewLoader returns a loader. builtin turns on the deferral of the built-in tools Classify marks Deferred; MCP
// sources decide for their own tools.
func NewLoader(builtin bool) *Loader {
	return &Loader{builtin: builtin, loaded: map[string]bool{}}
}

// DefersBuiltin reports whether the built-in tool is held back until loaded (whether or not it is loaded now).
func (l *Loader) DefersBuiltin(name string) bool {
	return l != nil && l.builtin && Classify(name) == Deferred
}

// BuiltinDeferral reports whether built-in tools are deferred in this session.
func (l *Loader) BuiltinDeferral() bool { return l != nil && l.builtin }

// AddSource adds a source of deferred tools.
func (l *Loader) AddSource(s Source) {
	if l == nil || s == nil {
		return
	}
	l.mu.Lock()
	l.sources = append(l.sources, s)
	l.mu.Unlock()
}

// OnLoad sets what runs when tools are newly loaded (the session records them in its transcript).
func (l *Loader) OnLoad(f func(names []string)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.onLoad = f
	l.mu.Unlock()
}

// Deferred returns the definitions of every deferred tool, loaded or not, sorted by name.
func (l *Loader) Deferred() []llm.ToolDef {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	sources := append([]Source(nil), l.sources...)
	l.mu.Unlock()
	seen := map[string]bool{}
	var out []llm.ToolDef
	for _, s := range sources {
		for _, d := range s.DeferredDefs() {
			if !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Pending returns the deferred tools not loaded yet, sorted by name.
func (l *Loader) Pending() []llm.ToolDef {
	var out []llm.ToolDef
	for _, d := range l.Deferred() {
		if !l.Loaded(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

// Def returns a deferred tool's definition.
func (l *Loader) Def(name string) (llm.ToolDef, bool) {
	for _, d := range l.Deferred() {
		if d.Name == name {
			return d, true
		}
	}
	return llm.ToolDef{}, false
}

// IsDeferred reports whether a source defers the tool now.
func (l *Loader) IsDeferred(name string) bool {
	_, ok := l.Def(name)
	return ok
}

// Load marks deferred tools as loaded and returns the newly loaded ones, in the order given. Names no source defers
// are skipped.
func (l *Loader) Load(names ...string) []string {
	if l == nil {
		return nil
	}
	deferred := map[string]bool{}
	for _, d := range l.Deferred() {
		deferred[d.Name] = true
	}
	l.mu.Lock()
	var added []string
	for _, n := range names {
		if !deferred[n] || l.loaded[n] {
			continue
		}
		l.loaded[n] = true
		l.order = append(l.order, n)
		added = append(added, n)
	}
	f := l.onLoad
	l.mu.Unlock()
	if len(added) > 0 && f != nil {
		f(added)
	}
	return added
}

// Restore marks tools as loaded without asking the sources or telling OnLoad: a resumed session's record. A name
// that is not deferred in this session costs nothing; it is never offered through the loader.
func (l *Loader) Restore(names []string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, n := range names {
		if n != "" && !l.loaded[n] {
			l.loaded[n] = true
			l.order = append(l.order, n)
		}
	}
}

// RestoreFromHistory loads what a conversation already used: the tools its ToolSearch calls loaded (replayed against
// the current deferred set) and the deferred tools it called. Sessions recorded before the transcript kept a record
// of loads come back this way.
func (l *Loader) RestoreFromHistory(history []llm.Message) {
	if l == nil {
		return
	}
	var names []string
	for _, msg := range history {
		for _, b := range msg.ToolUses() {
			if b.Name == Name {
				var in searchInput
				if json.Unmarshal(b.Input, &in) == nil {
					names = append(names, l.find(in)...)
				}
				continue
			}
			names = append(names, b.Name)
		}
	}
	f := l.swapOnLoad(nil)
	l.Load(names...)
	l.swapOnLoad(f)
}

func (l *Loader) swapOnLoad(f func([]string)) func([]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	old := l.onLoad
	l.onLoad = f
	return old
}

// Forget drops a tool from the loaded set (its MCP server no longer has it).
func (l *Loader) Forget(name string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded[name] {
		return
	}
	delete(l.loaded, name)
	for i, n := range l.order {
		if n == name {
			l.order = append(l.order[:i:i], l.order[i+1:]...)
			break
		}
	}
}

// Loaded reports whether the tool was loaded.
func (l *Loader) Loaded(name string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loaded[name]
}

// Order returns the loaded tools in load order: the order their definitions are appended to requests.
func (l *Loader) Order() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.order...)
}

// Reminder returns the vars of the deferred_tools reminder (the tools not loaded yet), or nil when there are none.
func (l *Loader) Reminder() map[string]string {
	pending := l.Pending()
	if len(pending) == 0 {
		return nil
	}
	lines := make([]string, 0, len(pending))
	for _, d := range pending {
		if s := Summary(d.Description); s != "" {
			lines = append(lines, d.Name+": "+s)
		} else {
			lines = append(lines, d.Name)
		}
	}
	return map[string]string{"tools": strings.Join(lines, "\n")}
}

// Summary is the one-line summary of a tool description: its first sentence, cut to a length budget.
func Summary(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = strings.TrimSpace(desc[:i])
	}
	desc = strings.TrimLeft(desc, "#-* ")
	// The first sentence: a period followed by a space (or the end), not one inside a word like "e.g.x".
	for i := 0; i < len(desc)-1; i++ {
		if desc[i] == '.' && unicode.IsSpace(rune(desc[i+1])) && !abbreviation(desc[:i]) {
			desc = desc[:i+1]
			break
		}
	}
	if len(desc) > maxSummary {
		cut := strings.LastIndexByte(desc[:maxSummary], ' ')
		if cut < maxSummary/2 {
			cut = maxSummary
		}
		desc = strings.TrimRight(desc[:cut], " ,;:") + "..."
	}
	return desc
}

// abbreviation reports whether text ends with an abbreviation whose period does not end a sentence.
func abbreviation(text string) bool {
	low := strings.ToLower(text)
	for _, a := range []string{"e.g", "i.e", "etc", "vs"} {
		if strings.HasSuffix(low, a) {
			return true
		}
	}
	return false
}
