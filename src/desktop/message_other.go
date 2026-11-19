//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
)

// tell writes a message to the console.
func tell(text string) {
	fmt.Println(text)
}

// fail tells what went wrong and waits, so that a window opened by a double click does not close before it is read.
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	fmt.Fprintln(os.Stderr, "Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}
