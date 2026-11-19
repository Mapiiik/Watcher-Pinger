//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The program has no console, so what it has to say comes in a window of its own.

// tell shows a message and waits for its OK.
func tell(text string) {
	box(text, windows.MB_ICONINFORMATION)
}

// fail tells what went wrong and ends the program.
func fail(format string, args ...any) {
	box(fmt.Sprintf(format, args...), windows.MB_ICONERROR)
	os.Exit(1)
}

func box(text string, icon uint32) {
	title, _ := windows.UTF16PtrFromString("Watcher Pinger")
	message, _ := windows.UTF16PtrFromString(text)
	_, _ = windows.MessageBox(0, message, title, windows.MB_OK|icon|windows.MB_SETFOREGROUND)
}
