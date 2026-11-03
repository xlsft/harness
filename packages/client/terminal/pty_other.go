//go:build !unix && !windows

package terminal

// DefaultShell has nothing to offer where terminals are unsupported.
func DefaultShell() (shell string, args []string) { return "", nil }

func startProc(startSpec) (proc, error) { return nil, ErrUnsupported }
