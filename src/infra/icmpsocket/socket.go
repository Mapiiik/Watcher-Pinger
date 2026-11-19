// Package icmpsocket pings IPv4 hosts over one ICMP socket: an unprivileged one where the system allows it, a raw one
// otherwise.
//
// The unprivileged socket (SOCK_DGRAM, IPPROTO_ICMP) needs no right but the group of the process in
// net.ipv4.ping_group_range, which Docker and the distributions with systemd allow. The kernel gives every request
// the echo ID of the socket and hands the socket only the replies with it, so a reply is found by its sequence
// number, and no other program pinging from the same machine sees it.
//
// The raw socket needs CAP_NET_RAW and sees the echo replies of every program on the machine. It is the engine of
// Watcher Agent: every request gets an echo ID of its own, starting at random, and the reply finds its request by it.
// A reply with that ID that is somebody else's, another address or another sequence number, is passed over.
package icmpsocket

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Kind is the kind of socket a pinger pings over.
type Kind string

const (
	// Auto takes an unprivileged socket where the system allows it, a raw one otherwise.
	Auto Kind = "auto"
	// Unprivileged is a SOCK_DGRAM socket of IPPROTO_ICMP.
	Unprivileged Kind = "unprivileged"
	// Raw is a SOCK_RAW socket, which needs CAP_NET_RAW.
	Raw Kind = "raw"
)

// payload tells the requests of the pinger apart in a capture.
var payload = []byte("watcher-pinger")

// Pinger shares one socket between the requests waiting at once.
type Pinger struct {
	kind Kind
	conn *icmp.PacketConn

	mu sync.Mutex
	// waiters are the requests waiting, by their sequence number on an unprivileged socket, by their ID on a raw one
	waiters map[int]chan reply

	id  atomic.Uint32
	seq atomic.Uint32
}

// reply is an echo reply as the read loop hands it over.
type reply struct {
	from net.IP
	seq  int
}

// Open opens a socket of the given kind, Auto trying an unprivileged one first.
func Open(kind Kind) (*Pinger, error) {
	switch kind {
	case Unprivileged:
		return open(Unprivileged, "udp4")
	case Raw:
		return open(Raw, "ip4:icmp")
	case Auto, "":
		p, err := open(Unprivileged, "udp4")
		if err == nil {
			return p, nil
		}
		p, rawErr := open(Raw, "ip4:icmp")
		if rawErr != nil {
			return nil, fmt.Errorf("neither an unprivileged ICMP socket (%v) nor a raw one (%w)", err, rawErr)
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unknown kind of ICMP socket %q, one of auto, unprivileged and raw expected", kind)
	}
}

func open(kind Kind, network string) (*Pinger, error) {
	conn, err := icmp.ListenPacket(network, "0.0.0.0")
	if err != nil {
		return nil, err
	}
	p := &Pinger{kind: kind, conn: conn, waiters: make(map[int]chan reply)}
	p.id.Store(rand.Uint32())
	p.seq.Store(rand.Uint32())
	go p.readLoop()
	return p, nil
}

// Kind is the kind of socket the pinger pings over.
func (p *Pinger) Kind() Kind {
	return p.kind
}

// Close closes the socket; the requests waiting then time out.
func (p *Pinger) Close() error {
	return p.conn.Close()
}

// Ping sends one echo request and waits for its reply. Without one in time ok is false and there is no error: an
// error means the request did not go out.
func (p *Pinger) Ping(ip net.IP, timeout time.Duration) (time.Duration, bool, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false, errors.New("only IPv4 addresses are pinged")
	}

	seq := int(p.seq.Add(1) & 0xffff)
	id, key := 0, seq
	var to net.Addr = &net.UDPAddr{IP: ip4}
	if p.kind == Raw {
		id = int(p.id.Add(1) & 0xffff)
		key = id
		to = &net.IPAddr{IP: ip4}
	}

	// room for a reply that is not ours as well as for ours
	replies := make(chan reply, 4)
	p.mu.Lock()
	p.waiters[key] = replies
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, key)
		p.mu.Unlock()
	}()

	request, err := (&icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: payload},
	}).Marshal(nil)
	if err != nil {
		return 0, false, err
	}

	start := time.Now()
	if _, err := p.conn.WriteTo(request, to); err != nil {
		return 0, false, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if !await(replies, timer.C, ip4, seq) {
		return 0, false, nil
	}
	return time.Since(start), true, nil
}

// await waits for the reply from the address with the sequence number, passing over the replies that are somebody
// else's, until the time is up.
func await(replies <-chan reply, timeUp <-chan time.Time, from net.IP, seq int) bool {
	for {
		select {
		case r := <-replies:
			if r.from.Equal(from) && r.seq == seq {
				return true
			}
		case <-timeUp:
			return false
		}
	}
}

// readLoop hands every echo reply to the request waiting for it and drops the rest, until the socket is closed.
func (p *Pinger) readLoop() {
	buf := make([]byte, 1500)

	for {
		n, peer, err := p.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// the replies of a passing failure are lost, the pinger stays
			time.Sleep(10 * time.Millisecond)
			continue
		}

		var from net.IP
		switch addr := peer.(type) {
		case *net.IPAddr:
			from = addr.IP
		case *net.UDPAddr:
			from = addr.IP
		default:
			continue
		}

		message, err := icmp.ParseMessage(1, buf[:n])
		if err != nil || message.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := message.Body.(*icmp.Echo)
		if !ok {
			continue
		}

		key := echo.Seq
		if p.kind == Raw {
			key = echo.ID
		}
		p.mu.Lock()
		replies, ok := p.waiters[key]
		p.mu.Unlock()
		if !ok {
			continue
		}

		select {
		case replies <- reply{from: from, seq: echo.Seq}:
		default:
		}
	}
}
