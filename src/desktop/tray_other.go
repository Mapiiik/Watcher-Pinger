//go:build !windows && !linux

package main

import "context"

// run keeps to the console, as there is no icon for the notification area of other systems yet.
func (d *desktop) run(ctx context.Context) error {
	return d.runConsole(ctx)
}
