package icmpsocket

import (
	"net"
	"sync"
	"testing"
	"time"
)

// opened opens a socket of the kind, or skips a test run where the system does not allow it.
func opened(t *testing.T, kind Kind) *Pinger {
	t.Helper()
	p, err := Open(kind)
	if err != nil {
		t.Skipf("no %s ICMP socket here: %v", kind, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

var kinds = []Kind{Unprivileged, Raw}

func TestTheLoopbackAnswers(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			p := opened(t, kind)

			rtt, ok, err := p.Ping(net.ParseIP("127.0.0.1"), time.Second)
			if err != nil || !ok {
				t.Fatalf("ok %v, err %v", ok, err)
			}
			if rtt <= 0 || rtt > time.Second {
				t.Errorf("round trip %v", rtt)
			}
		})
	}
}

// Requests waiting at once each get their own reply.
func TestRequestsAtOnceTakeTheirOwnReplies(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			p := opened(t, kind)

			var wg sync.WaitGroup
			failed := make(chan string, 64)
			for i := range 64 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ip := net.IPv4(127, 0, 0, byte(1+i))
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
		})
	}
}

// Two pingers on one machine do not take each other's replies, which a raw socket sees too: with the same echo IDs,
// two programs counting from the same place, each passes over the replies to the addresses of the other.
func TestTwoPingersKeepToTheirOwnReplies(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			pingers := []*Pinger{opened(t, kind), opened(t, kind)}
			pingers[1].id.Store(pingers[0].id.Load())
			pingers[1].seq.Store(pingers[0].seq.Load())

			var wg sync.WaitGroup
			failed := make(chan string, 128)
			for n, p := range pingers {
				for i := range 64 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						ip := net.IPv4(127, byte(1+n), 0, byte(1+i))
						if _, ok, err := p.Ping(ip, 2*time.Second); err != nil || !ok {
							failed <- ip.String()
						}
					}()
				}
			}
			wg.Wait()
			close(failed)

			lost := 0
			for range failed {
				lost++
			}
			if lost > 0 {
				t.Errorf("%d of 128 requests got no reply of their own", lost)
			}
		})
	}
}

// A reply that is somebody else's is passed over, and the request waits on for its own.
func TestAReplyOfSomebodyElseIsPassedOver(t *testing.T) {
	replies := make(chan reply, 4)
	ip := net.ParseIP("192.0.2.1").To4()
	replies <- reply{from: net.ParseIP("192.0.2.9").To4(), seq: 7}
	replies <- reply{from: ip, seq: 8}
	replies <- reply{from: ip, seq: 7}

	if !await(replies, time.After(time.Second), ip, 7) {
		t.Error("the own reply was not taken")
	}

	replies <- reply{from: ip, seq: 8}
	if await(replies, time.After(50*time.Millisecond), ip, 7) {
		t.Error("a reply of somebody else was taken")
	}
}

// An address nobody has is not answered, and that is no error.
func TestAnAddressNobodyHasIsNotAnswered(t *testing.T) {
	p := opened(t, Auto)

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

func TestAnUnknownKindIsRefused(t *testing.T) {
	if _, err := Open("icmpv5"); err == nil {
		t.Error("taken")
	}
}
