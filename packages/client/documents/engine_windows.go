//go:build windows

package documents

import "os/exec"

func setEngineProcess(cmd *exec.Cmd) {}

func fileLinkCount(st interface{ Sys() any }) uint64 { return 1 }
