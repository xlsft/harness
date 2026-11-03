package terminal

import (
	"errors"
	"io"
)

// ErrUnsupported is returned where this platform has no PTY support.
var ErrUnsupported = errors.New("terminals are not supported on this platform yet")

// proc is a shell running in a pseudo-terminal: Read gives its output, Write sends input.
type proc interface {
	io.ReadWriter
	Pid() int
	Resize(cols, rows uint16) error
	// Wait blocks until the process exits and returns its exit code (128 + signal when killed by a signal).
	Wait() (int, error)
	// Hangup asks the process (group) to end; Kill ends it for sure.
	Hangup() error
	Kill() error
	// Close releases the PTY.
	Close() error
}

// startSpec is what startProc needs to run a shell.
type startSpec struct {
	Shell      string
	Args       []string
	Dir        string
	Env        []string
	Cols, Rows uint16
}
