//go:build windows

package main

import "os/exec"

// openBrowser opens the page in the default browser.
func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
