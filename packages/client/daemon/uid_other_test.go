//go:build !linux

package daemon

func isLinux() bool { return false }
