// Package icmpraw pings IPv4 hosts over one raw ICMP socket, which needs CAP_NET_RAW on Linux.
//
// It is the engine of Watcher Agent (src/infra/icmpengine4) made into the pinger the monitor takes: every request gets
// an echo ID of its own, and the reply finds its request by it. The IDs start at random, so that another program
// pinging from the same network namespace, Watcher Agent among them, does not take the replies for its own.
package icmpraw

import (
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// payload tells the requests of the pinger apart in a capture.
var payload = []byte("watcher-pinger")

// Pinger shares one raw socket between the requests waiting at once.
type Pinger struct {
	conn *icmp.PacketConn

	mu      sync.Mutex
	waiters map[int]chan reply

	id  atomic.Uint32
	seq atomic.Uint32
}

// reply is an echo reply as the read loop hands it over.
type reply struct {
	from net.IP
	seq  int
}

// Open opens the socket and starts taking the replies.
func Open() (*Pinger, error) {
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, err
	}

	p := &Pinger{
		conn:    conn,
		waiters: make(map[int]chan reply),
	}
	p.id.Store(rand.Uint32())
	go p.readLoop()
	return p, nil
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

	id := int(p.id.Add(1) & 0xffff)
	seq := int(p.seq.Add(1) & 0xffff)
	replies := make(chan reply, 1)

	p.mu.Lock()
	p.waiters[id] = replies
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, id)
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
	if _, err := p.conn.WriteTo(request, &net.IPAddr{IP: ip4}); err != nil {
		return 0, false, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-replies:
		if !r.from.Equal(ip4) || r.seq != seq {
			return 0, false, nil
		}
		return time.Since(start), true, nil
	case <-timer.C:
		return 0, false, nil
	}
}

// readLoop hands every echo reply to the request waiting for its ID and drops the rest, until the socket is closed.
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

		from, ok := peer.(*net.IPAddr)
		if !ok {
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

		p.mu.Lock()
		replies, ok := p.waiters[echo.ID]
		p.mu.Unlock()
		if !ok {
			continue
		}

		select {
		case replies <- reply{from: from.IP, seq: echo.Seq}:
		default:
		}
	}
}
