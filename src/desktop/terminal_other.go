//go:build !linux && !windows

package main

// onTerminal takes a system other than Linux and Windows to have the pinger started from a terminal.
func onTerminal() bool {
	return true
}
