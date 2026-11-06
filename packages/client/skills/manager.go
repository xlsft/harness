package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

// Listing limits. The budget is a share of the context window: names and descriptions are in every request, so a pile of
// skills must not eat what the work needs.
const (
	// listingShare is the share of the context window, in characters at about four per token.
	listingSharePct = 1
	charsPerToken   = 4
	// DefaultListingBudget is used when the window is unknown; it is also what a share of a 200k window comes to.
	DefaultListingBudget = 8000
	minListingBudget     = 2000
	// maxEntryChars caps one description; budget pressure shortens it further.
	maxEntryChars = 400
	minEntryChars = 40
)

// BudgetFor returns the listing's size in characters for a context window in tokens; HARNESS_SKILL_LISTING_CHARS overrides it.
func BudgetFor(window int) int {
	if v, err := strconv.Atoi(os.Getenv("HARNESS_SKILL_LISTING_CHARS")); err == nil && v > 0 {
		return v
	}
	if window <= 0 {
		return DefaultListingBudget
	}
	return max(window*charsPerToken*listingSharePct/100, minListingBudget)
}

// ManagerOptions configure a session's Manager.
type ManagerOptions struct {
	// Place reads the session's folder and whether it is trusted, now.
	Place func() (cwd string, trusted bool)
	// Options reads where to look now: the session may have moved, the trust may have changed.
	Options func() Options
	// Hidden says the model may not see a name (a deny rule on the Skill tool).
	Hidden func(name string) bool
}

// Manager is a session's view of its skills: the catalog, what touching files has brought in, and the listing.
type Manager struct {
	o ManagerOptions

	mu      sync.Mutex
	cat     *Catalog
	nested  map[string]bool
	active  map[string]bool
	touched []string
	// override holds the user's latest choices made on this client, which the synced settings may not show yet.
	override map[string]bool
}

// NewManager creates a Manager; it scans on first use.
func NewManager(o ManagerOptions) *Manager {
	return &Manager{o: o, nested: map[string]bool{}, active: map[string]bool{}, override: map[string]bool{}}
}

// Catalog returns the entries: read again from their sources with refresh, as last read without.
func (m *Manager) Catalog(refresh bool) *Catalog {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cat == nil || refresh {
		m.scanLocked()
	}
	return m.cat
}

func (m *Manager) scanLocked() {
	o := m.o.Options()
	for d := range m.nested {
		o.Nested = append(o.Nested, d)
	}
	disabled := map[string]bool{}
	for k, v := range o.Disabled {
		disabled[k] = v
	}
	for k, off := range m.override {
		disabled[k] = off
	}
	o.Disabled = disabled
	m.cat = Discover(o)
	m.matchPathsLocked()
}

// SetEnabled applies a switch the user just flipped, ahead of the synced settings.
func (m *Manager) SetEnabled(name string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.override[name] = !enabled
	if m.cat != nil {
		m.scanLocked()
	}
}

// NoteFile hears about a file a tool is about to read or change: a folder on its way down from the session's may hold
// skills of its own, and skills with `paths` may apply now.
func (m *Manager) NoteFile(path string) {
	if path == "" {
		return
	}
	cwd, trusted := m.o.Place()
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.touched) < 10000 {
		m.touched = append(m.touched, path)
	}
	rescan := false
	if cwd != "" && trusted && within(path, cwd) {
		for d := filepath.Dir(path); d != cwd && within(d, cwd) && d != filepath.Dir(d); d = filepath.Dir(d) {
			if m.nested[d] || skipPath(d, cwd) {
				continue
			}
			for _, sub := range []string{filepath.Join(d, ".claude", "skills"), filepath.Join(d, ".harness", "skills")} {
				if st, err := os.Stat(sub); err == nil && st.IsDir() {
					m.nested[d], rescan = true, true
				}
			}
		}
	}
	if rescan || m.cat == nil {
		m.scanLocked()
	} else {
		m.matchPathsLocked()
	}
}

func skipPath(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if skip(part) {
			return true
		}
	}
	return false
}

func (m *Manager) matchPathsLocked() {
	if m.cat == nil {
		return
	}
	root, _ := m.o.Place()
	for _, s := range m.cat.All {
		if len(s.Paths) == 0 || m.active[s.Name] {
			continue
		}
		for _, p := range m.touched {
			if matchesPaths(s.Paths, p, root) {
				m.active[s.Name] = true
				break
			}
		}
	}
}

func matchesPaths(globs []string, path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, g := range globs {
		g = strings.TrimPrefix(g, "./")
		if ok, _ := doublestar.Match(g, rel); ok {
			return true
		}
		if !strings.Contains(g, "/") {
			if ok, _ := doublestar.Match(g, filepath.Base(rel)); ok {
				return true
			}
		}
	}
	return false
}

// ModelInvocable reports whether the model may run s: the switch, the frontmatter and the rules say yes. Paths only limit
// the listing: a skill the model knows by name still runs.
func (m *Manager) ModelInvocable(c *Catalog, s Skill) bool {
	return s.ShadowedBy == "" && c.Enabled(s.Name) && !s.DisableModelInvocation && (m.o.Hidden == nil || !m.o.Hidden(s.Name))
}

// Report says how the listing came out, for Extensions.
type Report struct {
	Budget, Used int
	// Listed holds the names in it.
	Listed map[string]bool
}

// Listing renders what the model reads for a context window of that many tokens: every skill it may run, one line each,
// "" for none. refresh reads the sources again. Under budget pressure descriptions shrink evenly, then go; names that
// still don't fit are left out, and the listing says how many.
func (m *Manager) Listing(window int, refresh bool) (string, Report) {
	c := m.Catalog(refresh)
	m.mu.Lock()
	active := make(map[string]bool, len(m.active))
	for k, v := range m.active {
		active[k] = v
	}
	m.mu.Unlock()
	var list []Skill
	for _, s := range c.All {
		if m.ModelInvocable(c, s) && (len(s.Paths) == 0 || active[s.Name]) {
			list = append(list, s)
		}
	}
	return renderListing(list, BudgetFor(window))
}

func entryLine(s Skill, descChars int) string {
	line := "- " + s.Name
	if s.ArgumentHint != "" {
		line += " " + s.ArgumentHint
	}
	desc := s.Description
	if s.WhenToUse != "" {
		desc = strings.TrimSpace(desc + " " + s.WhenToUse)
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if descChars <= 0 || desc == "" {
		return line
	}
	if r := []rune(desc); len(r) > descChars {
		desc = string(r[:descChars]) + "…"
	}
	return line + ": " + desc
}

func renderListing(list []Skill, budget int) (string, Report) {
	rep := Report{Budget: budget, Listed: map[string]bool{}}
	if len(list) == 0 {
		return "", rep
	}
	width := maxEntryChars
	render := func(w int) (string, int) {
		var b strings.Builder
		for _, s := range list {
			b.WriteString(entryLine(s, w))
			b.WriteByte('\n')
		}
		return b.String(), b.Len()
	}
	text, size := render(width)
	for size > budget && width > minEntryChars {
		width = max(width*3/4, minEntryChars)
		text, size = render(width)
	}
	if size > budget {
		width = 0
		text, size = render(0)
	}
	kept := list
	if size > budget {
		// Names alone don't fit: the first ones stay (the order ranks by source, then name).
		var b strings.Builder
		n := 0
		for _, s := range list {
			line := entryLine(s, 0) + "\n"
			if b.Len()+len(line) > budget {
				break
			}
			b.WriteString(line)
			n++
		}
		kept = list[:n]
		fmt.Fprintf(&b, "(and %d more: the user can name them, and Skill runs any of them by name)\n", len(list)-n)
		text = b.String()
	}
	for _, s := range kept {
		rep.Listed[s.Name] = true
	}
	text = strings.TrimRight(text, "\n")
	rep.Used = len(text)
	return text, rep
}
