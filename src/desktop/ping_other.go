//go:build !windows

package main

import (
	"fmt"

	"watcher-pinger/src/infra/icmpsocket"
)

// openPinger opens an ICMP socket for IPv4 and IPv6: an unprivileged one where the system allows it, as most
// distributions do, a raw one otherwise.
func openPinger() (pinger, error) {
	p, err := icmpsocket.OpenPingers(icmpsocket.Auto, nil)
	if err != nil {
		return nil, fmt.Errorf("%w (allow the group of the user in net.ipv4.ping_group_range, or give the program "+
			"the raw socket: sudo setcap cap_net_raw+ep watcher-pinger)", err)
	}
	return p, nil
}
