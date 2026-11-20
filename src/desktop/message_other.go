//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
)

// tell writes a message to the console, and shows it on the desktop where nobody watches the console.
func tell(text string) {
	fmt.Println(text)
	if !onTerminal() {
		toDesktop(text, false)
	}
}

// fail tells what went wrong and waits, so that a window opened by a double click does not close before it is read.
// Started from the menu of a desktop, with no terminal to write to, it shows the message there and ends.
func fail(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, text)
	if !onTerminal() {
		toDesktop(text, true)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}

// toDesktop shows the message in a window of zenity or kdialog, or as a notification, whichever the desktop has;
// an error waits for its OK. Without any of them the console is all there is.
func toDesktop(text string, isError bool) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	kind := map[bool][2]string{true: {"--error", "--error"}, false: {"--info", "--msgbox"}}[isError]
	for _, try := range [][]string{
		{"zenity", kind[0], "--title=Watcher Pinger", "--text=" + text},
		{"kdialog", "--title", "Watcher Pinger", kind[1], text},
		{"notify-send", "Watcher Pinger", text},
	} {
		if _, err := exec.LookPath(try[0]); err != nil {
			continue
		}
		cmd := exec.Command(try[0], try[1:]...)
		if isError {
			_ = cmd.Run()
		} else if cmd.Start() == nil {
			go func() { _ = cmd.Wait() }()
		}
		return
	}
}
