//go:build !windows

package icmpwin

import (
	"errors"
	"net"
	"time"
)

// Pinger stands in for the ICMP helper of Windows, which other systems do not have.
type Pinger struct{}

// Open says this system cannot ping this way.
func Open() (*Pinger, error) {
	return nil, errors.New("the ICMP helper exists on Windows only")
}

// Close does nothing.
func (p *Pinger) Close() error {
	return nil
}

// Ping cannot ping.
func (p *Pinger) Ping(net.IP, time.Duration) (time.Duration, bool, error) {
	return 0, false, errors.New("the ICMP helper exists on Windows only")
}
