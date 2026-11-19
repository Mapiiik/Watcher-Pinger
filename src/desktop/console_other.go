//go:build !windows

package main

// openConsole has nothing to do: the program writes to the console it was started from.
func openConsole() {}
