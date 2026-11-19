//go:build !windows

package main

import (
	"context"
	"fmt"
)

// run keeps the pinger going in the console until it is interrupted, or until the page stops.
func (d *desktop) run(ctx context.Context) error {
	fmt.Printf("The page is at %s, Ctrl+C stops the pinger.\n", d.url)
	select {
	case <-ctx.Done():
		return nil
	case err := <-d.stopped:
		return err
	}
}
