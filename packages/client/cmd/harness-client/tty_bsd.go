//go:build !windows && !linux

package main

import "golang.org/x/sys/unix"

const ttyRequest = unix.TIOCGETA
