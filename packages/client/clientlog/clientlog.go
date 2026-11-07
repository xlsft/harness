// Package clientlog is the client's own log: a size-capped file under $HARNESS_HOME/logs that rotates through a few
// older files, beside the warnings it has always written to stderr. The desktop app runs the client with nobody reading
// its stderr, so without the file nothing of a session that went wrong can be seen afterwards.
//
// What goes in: what the code logs through slog (session starts, sync, MCP, errors). What never goes in: tokens, keys,
// passwords and prompt or message text. Attributes with such names are replaced by "[redacted]", and values that look
// like harness tokens are masked wherever they appear (see redact).
package clientlog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/xlsft/harness/packages/client/config"
)

const (
	// FileName is the current log; older ones are FileName.1 (newest) to FileName.<Keep>.
	FileName = "client.log"
	// MaxBytes is the size at which the log rotates.
	MaxBytes = 4 << 20
	// Keep is how many rotated files stay.
	Keep = 3
)

// Dir is the folder of the client's logs.
func Dir() string { return filepath.Join(config.Home(), "logs") }

// Path is the current log file.
func Path() string { return filepath.Join(Dir(), FileName) }

// Writer appends to a file and rotates it once it reaches MaxBytes. Safe for concurrent use, within one process; another
// process writing the same file (a one-shot run beside serve) may rotate it too, which only costs a shorter file.
type Writer struct {
	Path     string
	MaxBytes int64
	Keep     int

	mu   sync.Mutex
	f    *os.File
	size int64
}

func (w *Writer) open() error {
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *Writer) rotate() error {
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	keep := w.Keep
	if keep <= 0 {
		keep = Keep
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", w.Path, keep))
	for i := keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.Path, i), fmt.Sprintf("%s.%d", w.Path, i+1))
	}
	_ = os.Rename(w.Path, w.Path+".1")
	return w.open()
}

// Write implements io.Writer.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	max := w.MaxBytes
	if max <= 0 {
		max = MaxBytes
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Options configure New.
type Options struct {
	// Mode names the process in every line: "serve", "print", "ci", ...
	Mode string
	// Verbose logs debug lines (to both); otherwise the file gets info and up, stderr warnings and up.
	Verbose bool
	// Stderr is where warnings have always gone (nil: none).
	Stderr io.Writer
	// File replaces the log file (tests); nil: a Writer on Path(). HARNESS_NO_LOG_FILE=1 turns the file off.
	File io.Writer
}

var (
	sharedMu sync.Mutex
	shared   *Writer
)

// fileWriter is the one Writer of this process for the log file.
func fileWriter() *Writer {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared == nil || shared.Path != Path() {
		shared = &Writer{Path: Path(), MaxBytes: MaxBytes, Keep: Keep}
	}
	return shared
}

// New returns the client's logger.
func New(o Options) *slog.Logger {
	stderrLevel, fileLevel := slog.LevelWarn, slog.LevelInfo
	if o.Verbose {
		stderrLevel, fileLevel = slog.LevelDebug, slog.LevelDebug
	}
	opts := func(l slog.Level) *slog.HandlerOptions {
		return &slog.HandlerOptions{Level: l, ReplaceAttr: redactAttr}
	}
	var hs []slog.Handler
	if o.Stderr != nil {
		hs = append(hs, slog.NewTextHandler(o.Stderr, opts(stderrLevel)))
	}
	file := o.File
	if file == nil && os.Getenv("HARNESS_NO_LOG_FILE") != "1" {
		file = fileWriter()
	}
	if file != nil {
		hs = append(hs, slog.NewTextHandler(file, opts(fileLevel)).WithAttrs([]slog.Attr{slog.String("mode", o.Mode), slog.Int("pid", os.Getpid())}))
	}
	return slog.New(fanout(hs))
}

// fanout sends each record to every handler that takes its level.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	return slices.ContainsFunc(f, func(h slog.Handler) bool { return h.Enabled(ctx, l) })
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (f fanout) WithAttrs(as []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(as)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// credentialWords anywhere in an attribute name, and textWords as the whole name or its last part, mark values that never
// reach a log: credentials, and the text of prompts and messages.
var (
	credentialWords = []string{"token", "secret", "password", "passwd", "authorization", "cookie", "apikey", "api_key", "credential"}
	textWords       = []string{"key", "prompt", "content", "text", "system", "input", "body", "message_text"}
)

func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, w := range credentialWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	for _, w := range textWords {
		if k == w || strings.HasSuffix(k, "_"+w) || strings.HasSuffix(k, "."+w) {
			return true
		}
	}
	return false
}

// tokenPattern finds harness tokens (hat_, hrt_, hdt_ and similar prefixes), bearer values and common provider keys.
var tokenPattern = regexp.MustCompile(`(?i)\b(h[a-z]t_[A-Za-z0-9_\-]{6,}|sk-[A-Za-z0-9_\-]{8,}|bearer\s+[A-Za-z0-9._\-]{8,})`)

// Redact masks token-like values in s.
func Redact(s string) string {
	return tokenPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(strings.ToLower(m), "bearer") {
			return "Bearer [redacted]"
		}
		if i := strings.Index(m, "_"); i > 0 && i < 4 {
			return m[:i+1] + "[redacted]"
		}
		return "[redacted]"
	})
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.MessageKey || a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.SourceKey {
		if a.Key == slog.MessageKey {
			return slog.String(a.Key, Redact(a.Value.String()))
		}
		return a
	}
	if secretKey(a.Key) {
		return slog.String(a.Key, "[redacted]")
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, Redact(err.Error()))
		}
		return slog.String(a.Key, Redact(a.Value.String()))
	}
	return a
}

// Tail returns the last n lines of the current log ("" when there is none).
func Tail(n int) string {
	data, err := os.ReadFile(Path())
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
