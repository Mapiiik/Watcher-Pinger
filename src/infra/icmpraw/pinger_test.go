package icmpraw

import (
	"net"
	"sync"
	"testing"
	"time"
)

// open opens the socket, or skips a test run without the right to it.
func open(t *testing.T) *Pinger {
	t.Helper()
	p, err := Open()
	if err != nil {
		t.Skipf("no raw socket here (CAP_NET_RAW is needed): %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestTheLoopbackAnswers(t *testing.T) {
	p := open(t)

	rtt, ok, err := p.Ping(net.ParseIP("127.0.0.1"), time.Second)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Errorf("round trip %v", rtt)
	}
}

// Requests waiting at once each get their own reply.
func TestRequestsAtOnceTakeTheirOwnReplies(t *testing.T) {
	p := open(t)

	var wg sync.WaitGroup
	failed := make(chan string, 64)
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ip := net.IPv4(127, 0, 0, byte(1+i%4))
			if _, ok, err := p.Ping(ip, 2*time.Second); err != nil || !ok {
				failed <- ip.String()
			}
		}()
	}
	wg.Wait()
	close(failed)

	for ip := range failed {
		t.Errorf("%s did not answer", ip)
	}
}

// An address nobody has is not answered, and that is no error.
func TestAnAddressNobodyHasIsNotAnswered(t *testing.T) {
	p := open(t)

	// TEST-NET-1, never routed
	_, ok, err := p.Ping(net.ParseIP("192.0.2.1"), 200*time.Millisecond)
	if ok {
		t.Error("192.0.2.1 answered")
	}
	if err != nil {
		// a machine without a default route cannot send it at all
		t.Logf("not sent: %v", err)
	}
}

// IPv6 is not pinged yet, which is an error rather than an address down.
func TestIPv6IsAnError(t *testing.T) {
	p := &Pinger{}

	if _, ok, err := p.Ping(net.ParseIP("2001:db8::1"), time.Second); err == nil || ok {
		t.Errorf("ok %v, err %v", ok, err)
	}
}

// The echo IDs of two pingers start apart.
func TestIDsStartAtRandom(t *testing.T) {
	starts := map[uint32]bool{}
	for range 8 {
		p, err := Open()
		if err != nil {
			t.Skipf("no raw socket here: %v", err)
		}
		starts[p.id.Load()] = true
		_ = p.Close()
	}
	if len(starts) < 2 {
		t.Errorf("eight pingers started at %v", starts)
	}
}
