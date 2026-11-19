//go:build windows

package main

import "watcher-pinger/src/infra/icmpwin"

// openPinger opens the ICMP helper of Windows, which needs no administrator.
func openPinger() (pinger, error) {
	p, err := icmpwin.Open()
	if err != nil {
		return nil, err
	}
	return p, nil
}
