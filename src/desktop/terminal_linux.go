//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// onTerminal tells whether the pinger writes to a terminal, rather than to the journal of a desktop that started it.
func onTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stderr.Fd()), unix.TCGETS)
	return err == nil
}
