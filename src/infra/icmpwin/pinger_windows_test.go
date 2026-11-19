//go:build windows

package icmpwin

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// nobody is an address of TEST-NET-1, which nothing on the internet answers.
var nobody = net.IPv4(192, 0, 2, 1)

func open(t *testing.T) *Pinger {
	t.Helper()

	pinger, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := pinger.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return pinger
}

func TestTheLoopbackAnswers(t *testing.T) {
	_, ok, err := open(t).Ping(net.IPv4(127, 0, 0, 1), time.Second)

	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v; want an answer", ok, err)
	}
}

func TestAnAddressNobodyHasIsNoAnswerAndNoError(t *testing.T) {
	_, ok, err := open(t).Ping(nobody, 300*time.Millisecond)

	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v; want neither", ok, err)
	}
}

func TestAnIPv6AddressIsRefused(t *testing.T) {
	if _, _, err := open(t).Ping(net.ParseIP("::1"), time.Second); err == nil {
		t.Fatal("want an error")
	}
}

func TestAClosedPingerSendsNothing(t *testing.T) {
	pinger, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := pinger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, _, err := pinger.Ping(net.IPv4(127, 0, 0, 1), time.Second); err == nil {
		t.Fatal("want an error")
	}
}

// The pings of one handle wait side by side: a thousand addresses nobody has take a few timeouts, not a thousand.
func TestPingsOfOneHandleWaitSideBySide(t *testing.T) {
	pinger := open(t)
	timeout := 500 * time.Millisecond
	const count = 1000

	var wg sync.WaitGroup
	results := make([]string, count)
	start := time.Now()

	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ip := net.IPv4(192, 0, 2, byte(i%254+1))
			if i%20 == 0 {
				ip = net.IPv4(127, 0, 0, 1)
			}
			_, ok, err := pinger.Ping(ip, timeout)
			results[i] = fmt.Sprintf("%v %v", ok, err)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	answered := 0
	for i, result := range results {
		want := "false <nil>"
		if i%20 == 0 {
			want = "true <nil>"
			answered++
		}
		if result != want {
			t.Errorf("ping %d: %s, want %s", i, result, want)
		}
	}

	t.Logf("%d pings, %d answered, in %v", count, answered, elapsed)
	if elapsed > 10*timeout {
		t.Errorf("took %v, want them side by side", elapsed)
	}
}
