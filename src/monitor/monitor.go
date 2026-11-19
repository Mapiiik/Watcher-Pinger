// Package monitor pings a set of addresses over and over and keeps what it last learned about each.
//
// Addresses being watched, those on the screen, are pinged more often than the rest. Every change of a state gets a
// version, so that anyone can ask for what changed since the version they last saw.
package monitor

import (
	"context"
	"math/rand/v2"
	"net"
	"sort"
	"sync"
	"time"
)

// Pinger sends one echo request and waits for its reply. Without one in time ok is false and there is no error: an
// error means the request did not go out.
type Pinger interface {
	Ping(ip net.IP, timeout time.Duration) (time.Duration, bool, error)
}

// Status is what an address is believed to be.
type Status uint8

const (
	// Unknown has not been pinged yet.
	Unknown Status = iota
	// Up answered the last ping.
	Up
	// Down missed at least LostToBeDown pings in a row, or the first one ever.
	Down
	// Failed could not be pinged at all, an IPv6 address on a pinger without IPv6, say.
	Failed
)

// LostToBeDown is how many pings in a row an address that answered before must miss to be down, so that one lost
// packet does not raise an alarm.
const LostToBeDown = 2

// HistorySize is how many of the last pings the loss and the round trip times are reckoned over.
const HistorySize = 20

// State is what is known about one address: the last ping, and the last pings that went out, at most HistorySize,
// of which Received were answered.
type State struct {
	Status    Status
	RTT       time.Duration
	Lost      int
	LastReply time.Time
	Since     time.Time
	Sent      int
	Received  int
	RTTAvg    time.Duration
	RTTMax    time.Duration
	version   uint64
}

// history keeps the round trip times of the last pings that went out, a negative one for a ping not answered.
type history struct {
	rtts  [HistorySize]time.Duration
	count int
	next  int
}

func (h *history) add(rtt time.Duration, ok bool) {
	if !ok {
		rtt = -1
	}
	h.rtts[h.next] = rtt
	h.next = (h.next + 1) % HistorySize
	h.count = min(h.count+1, HistorySize)
}

// sum works the history into the state.
func (h *history) sum(state *State) {
	state.Sent, state.Received, state.RTTAvg, state.RTTMax = h.count, 0, 0, 0

	var total time.Duration
	for _, rtt := range h.rtts[:h.count] {
		if rtt < 0 {
			continue
		}
		state.Received++
		total += rtt
		state.RTTMax = max(state.RTTMax, rtt)
	}
	if state.Received > 0 {
		state.RTTAvg = total / time.Duration(state.Received)
	}
}

// Options say how often and how patiently to ping: an address watched most often, a device of the network, which
// tells where an outage starts, more often than the rest.
type Options struct {
	Timeout         time.Duration
	WatchedInterval time.Duration
	DeviceInterval  time.Duration
	IdleInterval    time.Duration
	Workers         int
}

// DefaultOptions let a few hundred pings wait at once. An address watched is down within ten seconds, two pings, of
// going quiet, a device within a minute. A ping takes a few milliseconds of the processor on some machines, so the
// rest is pinged once a minute.
var DefaultOptions = Options{
	Timeout:         time.Second,
	WatchedInterval: 5 * time.Second,
	DeviceInterval:  30 * time.Second,
	IdleInterval:    time.Minute,
	Workers:         512,
}

// jitter spreads the next ping of an address over a tenth of its interval either way, so that addresses pinged
// together at the start do not keep coming due together.
const jitter = 0.1

type target struct {
	ip      net.IP
	state   State
	history history
	due     time.Time
	busy    bool
	watched bool
	device  bool
}

// Monitor pings its addresses until its context ends.
type Monitor struct {
	pinger  Pinger
	options Options
	now     func() time.Time

	mu      sync.Mutex
	targets map[string]*target
	version uint64
}

// New pings the given addresses, the devices of the network among them, none of them pinged yet.
func New(pinger Pinger, ips []string, devices []string, options Options) *Monitor {
	m := &Monitor{
		pinger:  pinger,
		options: options,
		now:     time.Now,
		targets: make(map[string]*target, len(ips)),
	}
	for _, ip := range ips {
		m.targets[ip] = &target{ip: net.ParseIP(ip)}
	}
	for _, ip := range devices {
		if t, ok := m.targets[ip]; ok {
			t.device = true
		}
	}
	return m
}

// Run pings until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	jobs := make(chan *target)
	var wg sync.WaitGroup
	for range m.options.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				m.ping(t)
			}
		}()
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer func() {
		ticker.Stop()
		close(jobs)
		wg.Wait()
	}()

	for {
		for _, t := range m.due(m.options.Workers) {
			select {
			case jobs <- t:
			case <-ctx.Done():
				m.release(t)
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// due takes up to limit addresses whose turn it is, watched ones first and the longest waiting next, and marks them
// busy. Taking a few at a time lets an address watched meanwhile go before the rest of a long round.
func (m *Monitor) due(limit int) []*target {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	var due []*target
	for _, t := range m.targets {
		if !t.busy && !t.due.After(now) {
			due = append(due, t)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].watched != due[j].watched {
			return due[i].watched
		}
		return due[i].due.Before(due[j].due)
	})

	if len(due) > limit {
		due = due[:limit]
	}
	for _, t := range due {
		t.busy = true
	}
	return due
}

func (m *Monitor) release(t *target) {
	m.mu.Lock()
	t.busy = false
	m.mu.Unlock()
}

func (m *Monitor) ping(t *target) {
	rtt, ok, err := m.pinger.Ping(t.ip, m.options.Timeout)

	m.mu.Lock()
	defer m.mu.Unlock()

	first := t.state.Status == Unknown
	m.record(t, rtt, ok, err)
	t.busy = false
	t.due = m.now().Add(m.interval(t, first))
}

// interval is how long until the next ping. After the first one, all sent at the start, the next one comes anywhere
// within the interval, which spreads the pings of the rounds after evenly over it.
func (m *Monitor) interval(t *target, first bool) time.Duration {
	interval := m.options.IdleInterval
	switch {
	case t.watched:
		interval = m.options.WatchedInterval
	case t.device:
		interval = m.options.DeviceInterval
	}
	if first {
		return time.Duration(float64(interval) * rand.Float64())
	}
	return time.Duration(float64(interval) * (1 - jitter + 2*jitter*rand.Float64()))
}

// record works the result of a ping into the state of its address. The caller holds the lock.
func (m *Monitor) record(t *target, rtt time.Duration, ok bool, err error) {
	now := m.now()
	next := t.state

	switch {
	case err != nil:
		next.Status = Failed
		next.RTT = 0
	case ok:
		next.Status = Up
		next.RTT = rtt
		next.Lost = 0
		next.LastReply = now
	default:
		next.Lost++
		next.RTT = 0
		if t.state.Status != Up || next.Lost >= LostToBeDown {
			next.Status = Down
		}
	}

	if next.Status != t.state.Status {
		next.Since = now
	}

	// a ping that did not go out says nothing of the line, so it is left out of the loss
	if err == nil {
		t.history.add(rtt, ok)
		t.history.sum(&next)
	}

	// the time of the last reply moves with every answer and is shown only once there are none, so it is kept
	// without being told as a change
	if next.Status != t.state.Status || next.RTT != t.state.RTT || next.Lost != t.state.Lost ||
		next.Sent != t.state.Sent || next.Received != t.state.Received || next.RTTAvg != t.state.RTTAvg ||
		next.RTTMax != t.state.RTTMax {
		m.version++
		next.version = m.version
	}
	t.state = next
}

// Watch makes the given addresses the watched ones, pinged more often, and the others idle. An address watched anew is
// pinged right away.
func (m *Monitor) Watch(ips []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	watched := make(map[string]bool, len(ips))
	for _, ip := range ips {
		watched[ip] = true
	}

	now := m.now()
	for ip, t := range m.targets {
		was := t.watched
		t.watched = watched[ip]
		if t.watched && !was && t.due.After(now) {
			t.due = now
		}
	}
}

// Snapshot returns every state and the version it stands at.
func (m *Monitor) Snapshot() (map[string]State, uint64) {
	return m.collect(func(*target) bool { return true })
}

// Since returns the states changed after the given version and the version they bring it to.
func (m *Monitor) Since(version uint64) (map[string]State, uint64) {
	return m.collect(func(t *target) bool { return t.state.version > version })
}

func (m *Monitor) collect(wanted func(*target) bool) (map[string]State, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	states := make(map[string]State)
	for ip, t := range m.targets {
		if wanted(t) {
			states[ip] = t.state
		}
	}
	return states, m.version
}
