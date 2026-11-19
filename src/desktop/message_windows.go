//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The program has no console unless -console gives it one, so what it has to say otherwise comes in a window of its
// own.

// tell shows a message and waits for its OK, or writes it to the console.
func tell(text string) {
	if console {
		fmt.Println(text)
		return
	}
	box(text, windows.MB_ICONINFORMATION)
}

// fail tells what went wrong and ends the program. A console of its own closes with it, so it waits to be read.
func fail(format string, args ...any) {
	if console {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		if ownConsole {
			fmt.Fprintln(os.Stderr, "Press Enter to close.")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
		os.Exit(1)
	}
	box(fmt.Sprintf(format, args...), windows.MB_ICONERROR)
	os.Exit(1)
}

func box(text string, icon uint32) {
	title, _ := windows.UTF16PtrFromString("Watcher Pinger")
	message, _ := windows.UTF16PtrFromString(text)
	_, _ = windows.MessageBox(0, message, title, windows.MB_OK|icon|windows.MB_SETFOREGROUND)
}
