//go:build !windows

package main

import "os/exec"

// openBrowser opens the page in the default browser. xdg-open is waited for, so that it leaves no zombie behind.
func openBrowser(url string) error {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
