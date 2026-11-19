//go:build !windows

package main

// setUpConsole has nothing to do: the program writes to the terminal it was started from.
func setUpConsole(bool) {}
