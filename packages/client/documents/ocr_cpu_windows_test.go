//go:build windows

package documents

import "time"

// processCPU is not measured on Windows; benchmarks report wall time alone there.
func processCPU() time.Duration { return 0 }
