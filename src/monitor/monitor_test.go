package monitor

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// script answers each address as told, the same way every time.
type script struct {
	mu      sync.Mutex
	answers map[string]bool
	errs    map[string]error
	pinged  map[string]int
}

func (s *script) Ping(ip net.IP, _ time.Duration) (time.Duration, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pinged[ip.String()]++
	if err := s.errs[ip.String()]; err != nil {
		return 0, false, err
	}
	if s.answers[ip.String()] {
		return 3 * time.Millisecond, true, nil
	}
	return 0, false, nil
}

func newScript() *script {
	return &script{answers: map[string]bool{}, errs: map[string]error{}, pinged: map[string]int{}}
}

func TestAStateFollowsThePings(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1"}, nil, DefaultOptions)
	clock := time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	tg := m.targets["10.0.0.1"]

	steps := []struct {
		ok     bool
		err    error
		status Status
		lost   int
	}{
		{ok: false, status: Down, lost: 1}, // never answered: down at once
		{ok: true, status: Up, lost: 0},
		{ok: false, status: Up, lost: 1}, // one lost packet is no outage
		{ok: false, status: Down, lost: 2},
		{ok: true, status: Up, lost: 0},
		{err: errors.New("cannot send"), status: Failed, lost: 0},
	}

	for i, step := range steps {
		clock = clock.Add(time.Second)
		m.record(tg, time.Millisecond, step.ok, step.err)

		if tg.state.Status != step.status || tg.state.Lost != step.lost {
			t.Fatalf("step %d: status %d lost %d, want %d and %d", i, tg.state.Status, tg.state.Lost, step.status, step.lost)
		}
	}
	if !tg.state.Since.Equal(clock) {
		t.Errorf("since %v, want the moment it failed", tg.state.Since)
	}
}

// The loss and the round trip times are reckoned over the last pings that went out.
func TestTheLossIsReckonedOverTheLastPings(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1"}, nil, DefaultOptions)
	tg := m.targets["10.0.0.1"]

	m.record(tg, 10*time.Millisecond, true, nil)
	m.record(tg, 0, false, nil)
	m.record(tg, 0, false, errors.New("cannot send"))
	m.record(tg, 30*time.Millisecond, true, nil)

	got := tg.state
	if got.Sent != 3 || got.Received != 2 || got.RTTAvg != 20*time.Millisecond || got.RTTMax != 30*time.Millisecond {
		t.Fatalf("sent %d received %d avg %v max %v, want 3, 2, 20ms and 30ms",
			got.Sent, got.Received, got.RTTAvg, got.RTTMax)
	}

	// the window moves on: the lost one drops out after HistorySize more
	for range HistorySize {
		m.record(tg, 5*time.Millisecond, true, nil)
	}
	got = tg.state
	if got.Sent != HistorySize || got.Received != HistorySize || got.RTTMax != 5*time.Millisecond {
		t.Errorf("sent %d received %d max %v, want all %d answered at 5ms", got.Sent, got.Received, got.RTTMax,
			HistorySize)
	}
}

func TestOnlyWhatChangedIsHandedOut(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1", "10.0.0.2"}, nil, DefaultOptions)

	all, version := m.Snapshot()
	if len(all) != 2 || version != 0 {
		t.Fatalf("got %d states at version %d, want both at 0", len(all), version)
	}

	m.record(m.targets["10.0.0.1"], time.Millisecond, true, nil)
	changed, next := m.Since(version)
	if len(changed) != 1 || changed["10.0.0.1"].Status != Up || next != 1 {
		t.Fatalf("got %v at version %d, want the first up at 1", changed, next)
	}

	// the same answer again, once the history is full of it, is no change
	for range HistorySize {
		m.record(m.targets["10.0.0.1"], time.Millisecond, true, nil)
	}
	_, next = m.Since(next)
	m.record(m.targets["10.0.0.1"], time.Millisecond, true, nil)
	if changed, _ := m.Since(next); len(changed) != 0 {
		t.Errorf("got %v, want nothing", changed)
	}
}

func TestWatchedAddressesGoFirst(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, nil, DefaultOptions)
	m.Watch("page", []string{"10.0.0.3"})

	due := m.due(2)
	if len(due) != 2 || due[0].ip.String() != "10.0.0.3" {
		t.Fatalf("due %v, want the watched one first and two at most", due)
	}
	if again := m.due(10); len(again) != 1 {
		t.Errorf("due %d more, want the one not taken yet", len(again))
	}
}

func spread(m *Monitor, tg *target, first bool) (time.Duration, time.Duration) {
	low, high := time.Duration(1<<62), time.Duration(0)
	for range 1000 {
		interval := m.interval(tg, first)
		low, high = min(low, interval), max(high, interval)
	}
	return low, high
}

func TestTheNextPingIsSpreadAroundTheInterval(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1"}, nil, DefaultOptions)
	idle := DefaultOptions.IdleInterval

	low, high := spread(m, m.targets["10.0.0.1"], false)

	if low < idle*9/10 || high > idle*11/10 || high-low < idle/10 {
		t.Errorf("intervals between %v and %v, want them spread over %v ± 10 %%", low, high, idle)
	}
}

// All pinged at the start, the addresses come due anywhere within the interval after, rather than all together again.
func TestAfterTheFirstPingTheNextComesAnywhereWithinTheInterval(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1"}, nil, DefaultOptions)
	idle := DefaultOptions.IdleInterval

	low, high := spread(m, m.targets["10.0.0.1"], true)

	if low > idle/10 || high < idle*9/10 || high > idle {
		t.Errorf("intervals between %v and %v, want them anywhere within %v", low, high, idle)
	}
}

// A device is pinged more often than a customer, and anything watched more often than both.
func TestEachKindOfAddressHasItsInterval(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, []string{"10.0.0.2", "10.0.0.3"},
		DefaultOptions)
	m.Watch("page", []string{"10.0.0.3"})

	for ip, want := range map[string]time.Duration{
		"10.0.0.1": DefaultOptions.IdleInterval,
		"10.0.0.2": DefaultOptions.DeviceInterval,
		"10.0.0.3": DefaultOptions.WatchedInterval,
	} {
		if low, high := spread(m, m.targets[ip], false); low < want*9/10 || high > want*11/10 {
			t.Errorf("%s: intervals between %v and %v, want about %v", ip, low, high, want)
		}
	}
}

func TestAnAddressWatchedAnewIsPingedAtOnce(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1"}, nil, DefaultOptions)
	tg := m.targets["10.0.0.1"]
	tg.due = m.now().Add(time.Hour)

	m.Watch("page", []string{"10.0.0.1"})

	if tg.due.After(m.now()) {
		t.Errorf("due %v, want now", tg.due)
	}
}

func TestRunPingsEveryAddress(t *testing.T) {
	pinger := newScript()
	pinger.answers["10.0.0.1"] = true
	m := New(pinger, []string{"10.0.0.1", "10.0.0.2"}, nil, Options{
		Timeout: time.Millisecond, WatchedInterval: time.Hour, IdleInterval: time.Hour, Workers: 4,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		states, _ := m.Snapshot()
		if states["10.0.0.1"].Status == Up && states["10.0.0.2"].Status == Down {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("states %v", states)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done
}

// Two pages open watch what either of them shows, and a page closed stops watching.
func TestWhatAnyViewerWatchesIsWatched(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, nil, DefaultOptions)

	m.Watch("first", []string{"10.0.0.1"})
	m.Watch("second", []string{"10.0.0.2"})
	watched := func() map[string]bool {
		got := map[string]bool{}
		for ip, tg := range m.targets {
			if tg.watched {
				got[ip] = true
			}
		}
		return got
	}
	if got := watched(); len(got) != 2 || !got["10.0.0.1"] || !got["10.0.0.2"] {
		t.Fatalf("watched %v, want what both pages show", got)
	}

	m.Watch("first", []string{"10.0.0.3"})
	m.Forget("second")
	if got := watched(); len(got) != 1 || !got["10.0.0.3"] {
		t.Errorf("watched %v, want what the first page shows now", got)
	}
}

// New data keep what was learned of the addresses that stay, ping the new ones right away and drop the rest.
func TestNewDataKeepWhatStays(t *testing.T) {
	m := New(newScript(), []string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.2"}, DefaultOptions)
	m.Watch("page", []string{"10.0.0.3"})
	kept := m.targets["10.0.0.1"]
	m.record(kept, time.Millisecond, true, nil)
	kept.due = m.now().Add(time.Hour)

	m.Replace([]string{"10.0.0.1", "10.0.0.3"}, []string{"10.0.0.1"})

	if m.targets["10.0.0.1"] != kept || kept.state.Status != Up || kept.state.Sent != 1 {
		t.Errorf("the address kept lost what was learned: %+v", kept.state)
	}
	if !kept.device {
		t.Error("the address kept is not a device now")
	}
	if _, ok := m.targets["10.0.0.2"]; ok {
		t.Error("the address left out is still pinged")
	}
	added := m.targets["10.0.0.3"]
	if added == nil || added.due.After(m.now()) || !added.watched {
		t.Errorf("the new address is not due now and watched: %+v", added)
	}
	if all, _ := m.Snapshot(); len(all) != 2 {
		t.Errorf("%d states, want the two addresses pinged now", len(all))
	}
}
