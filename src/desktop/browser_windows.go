//go:build windows

package main

import "os/exec"

// openBrowser opens the page in the default browser.
func openBrowser(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	// waited for, so that its process is let go of
	go func() { _ = cmd.Wait() }()
	return nil
}
