package preview

import (
	"regexp"
	"strings"
	"sync"
)

// Log buffer limits: the oldest lines go first.
const (
	maxLogLines = 5000
	maxLogBytes = 2 << 20
	maxLineLen  = 4096
)

var (
	ansiPattern  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	errorPattern = regexp.MustCompile(`\b[A-Z]\w*(Error|Exception)\b|(?i:\b(error|errors|err|exception|fatal|panic|failed|failure|traceback|uncaught|unhandled|crash(ed)?)\b)`)
)

// logRing keeps a server's recent output as lines: stdout and stderr in the order they were written, escape
// sequences removed, always valid UTF-8 (the lines travel in proto strings).
type logRing struct {
	mu      sync.Mutex
	lines   []string
	size    int
	partial []byte
	dropped bool
}

// Write takes raw output. A carriage return ends a line too (progress bars).
func (l *logRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range p {
		switch {
		case c == '\n' || c == '\r':
			if len(l.partial) > 0 {
				l.commit()
			}
		case len(l.partial) >= maxLineLen:
			l.commit()
			l.partial = append(l.partial, c)
		default:
			l.partial = append(l.partial, c)
		}
	}
	return len(p), nil
}

func (l *logRing) commit() {
	line := cleanLine(l.partial)
	l.partial = l.partial[:0]
	if line == "" {
		return
	}
	l.lines = append(l.lines, line)
	l.size += len(line)
	for len(l.lines) > maxLogLines || l.size > maxLogBytes {
		l.size -= len(l.lines[0])
		l.lines = l.lines[1:]
		l.dropped = true
	}
}

func cleanLine(b []byte) string {
	return strings.ToValidUTF8(ansiPattern.ReplaceAllString(string(b), ""), "�")
}

// LogFilter selects lines.
type LogFilter struct {
	// Lines is how many of the last matching lines to return.
	Lines int
	// ErrorsOnly keeps lines that look like errors.
	ErrorsOnly bool
	// Search keeps lines containing this text, ignoring case.
	Search string
}

// tail returns the last matching lines, including an unfinished last line, and whether older output is gone.
func (l *logRing) tail(f LogFilter) ([]string, bool) {
	l.mu.Lock()
	all := append([]string(nil), l.lines...)
	if len(l.partial) > 0 {
		if line := cleanLine(l.partial); line != "" {
			all = append(all, line)
		}
	}
	dropped := l.dropped
	l.mu.Unlock()
	search := strings.ToLower(f.Search)
	out := all[:0]
	for _, line := range all {
		if f.ErrorsOnly && !errorPattern.MatchString(line) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(line), search) {
			continue
		}
		out = append(out, line)
	}
	if f.Lines > 0 && len(out) > f.Lines {
		out = out[len(out)-f.Lines:]
	}
	return out, dropped
}
