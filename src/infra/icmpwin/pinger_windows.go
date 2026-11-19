//go:build windows

// Package icmpwin pings through the ICMP helper of Windows, which needs no administrator, unlike a raw socket.
//
// Requests go out from a few threads of the pinger's own, each taking the replies to its requests as APCs while it
// waits. A thread waiting in IcmpSendEcho for each request costs the processor ten times as much once there are a
// couple of hundred of them, as every one is woken whenever any request is done. Sending a request to another host
// takes a couple of milliseconds of the processor, so a single thread would hold up the replies behind a long round.
package icmpwin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile   = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle  = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho2    = iphlpapi.NewProc("IcmpSendEcho2")
	procIcmpParseReplies = iphlpapi.NewProc("IcmpParseReplies")

	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procWaitForSingleObjectEx = kernel32.NewProc("WaitForSingleObjectEx")
)

// ipOptionInformation is IP_OPTION_INFORMATION.
type ipOptionInformation struct {
	TTL         uint8
	TOS         uint8
	Flags       uint8
	OptionsSize uint8
	OptionsData uintptr
}

// icmpEchoReply is ICMP_ECHO_REPLY.
type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

const (
	// ipSuccess is the status of an echo reply from the address asked.
	ipSuccess = 0

	// ipStatusBase is where the IP_STATUS codes start. With one of them the request went out and nothing, or
	// something other than the address asked, answered it.
	ipStatusBase = 11000
	ipStatusEnd  = 12000

	// maxSenders bounds the threads sending requests, half the processors otherwise.
	maxSenders = 8

	// spillAt is how many requests may wait for the first thread before the others take some.
	spillAt = 16
)

var (
	payload = []byte("watcher-pinger")

	// replySize leaves room for the reply, its data and an ICMP error message.
	replySize = int(unsafe.Sizeof(icmpEchoReply{})) + len(payload) + 8 + 8

	errClosed = errors.New("the pinger is closed")
)

type result struct {
	rtt time.Duration
	ok  bool
	err error
}

type request struct {
	address uint32
	timeout uint32
	reply   []byte
	done    chan result
}

// Pinger sends echo requests through the ICMP helper. Any number of goroutines may ping at once.
type Pinger struct {
	senders []*sender
	next    atomic.Uint64
	quit    chan struct{}
}

// sender is one thread sending requests over a handle of its own and taking the replies to them.
type sender struct {
	handle   uintptr
	wake     windows.Handle
	queue    chan *request
	quit     chan struct{}
	stopped  chan struct{}
	callback uintptr

	// what the thread of the sender alone touches
	pending map[uintptr]*request
	nextID  uintptr
}

// Open starts the threads that send the requests.
func Open() (*Pinger, error) {
	p := &Pinger{quit: make(chan struct{})}
	for range min(max(runtime.NumCPU()/2, 1), maxSenders) {
		s, err := newSender(p.quit)
		if err != nil {
			_ = p.Close()
			return nil, err
		}
		p.senders = append(p.senders, s)
	}
	return p, nil
}

func newSender(quit chan struct{}) (*sender, error) {
	handle, _, err := procIcmpCreateFile.Call()
	if handle == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("IcmpCreateFile: %w", err)
	}
	wake, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_, _, _ = procIcmpCloseHandle.Call(handle)
		return nil, fmt.Errorf("CreateEvent: %w", err)
	}

	s := &sender{
		handle:  handle,
		wake:    wake,
		queue:   make(chan *request, 4096),
		quit:    quit,
		stopped: make(chan struct{}),
		pending: make(map[uintptr]*request),
	}
	s.callback = windows.NewCallback(s.completed)
	go s.run()
	return s, nil
}

// Close lets the requests on their way finish and gives the handles back.
func (p *Pinger) Close() error {
	close(p.quit)

	var failed error
	for _, s := range p.senders {
		_ = windows.SetEvent(s.wake)
		<-s.stopped
		_ = windows.CloseHandle(s.wake)
		if ok, _, err := procIcmpCloseHandle.Call(s.handle); ok == 0 {
			failed = fmt.Errorf("IcmpCloseHandle: %w", err)
		}
	}
	return failed
}

// Ping sends one echo request and waits for its reply. Without one in time, or with one from elsewhere, ok is false
// and there is no error: an error means the request did not go out. The helper measures in whole milliseconds.
func (p *Pinger) Ping(ip net.IP, timeout time.Duration) (time.Duration, bool, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false, errors.New("not an IPv4 address")
	}

	milliseconds := uint32(timeout / time.Millisecond)
	if milliseconds == 0 {
		milliseconds = 1
	}
	r := &request{
		address: binary.LittleEndian.Uint32(ip4),
		timeout: milliseconds,
		reply:   make([]byte, replySize),
		done:    make(chan result, 1),
	}

	// the first thread sends what comes in ones and twos, the others help with a queue, at the start above all: the
	// more threads send at once, the more a request costs the processor
	s := p.senders[0]
	if len(s.queue) > spillAt && len(p.senders) > 1 {
		s = p.senders[1+p.next.Add(1)%uint64(len(p.senders)-1)]
	}
	select {
	case s.queue <- r:
	case <-p.quit:
		return 0, false, errClosed
	}
	_ = windows.SetEvent(s.wake)

	select {
	case res := <-r.done:
		return res.rtt, res.ok, res.err
	case <-s.stopped:
		return 0, false, errClosed
	}
}

// run sends what is asked and waits, alertable, for the next request or a reply, until closed and nothing is on its
// way any more.
func (s *sender) run() {
	// the replies come as APCs to the thread that sent the requests, so it stays the same one
	runtime.LockOSThread()
	defer close(s.stopped)

	for {
		for queued := true; queued; {
			select {
			case r := <-s.queue:
				s.send(r)
				// the replies that came meanwhile are handed over rather than kept until the queue is empty
				windows.SleepEx(0, true)
			default:
				queued = false
			}
		}

		select {
		case <-s.quit:
			if len(s.pending) == 0 {
				return
			}
		default:
		}

		// WAIT_IO_COMPLETION after replies, WAIT_OBJECT_0 after a request; both go round again
		_, _, _ = procWaitForSingleObjectEx.Call(uintptr(s.wake), uintptr(windows.INFINITE), 1)
	}
}

func (s *sender) send(r *request) {
	id := s.nextID
	s.nextID++
	s.pending[id] = r

	sent, _, err := procIcmpSendEcho2.Call(
		s.handle,
		0,
		s.callback,
		id,
		uintptr(r.address),
		uintptr(unsafe.Pointer(&payload[0])),
		uintptr(len(payload)),
		0,
		uintptr(unsafe.Pointer(&r.reply[0])),
		uintptr(len(r.reply)),
		uintptr(r.timeout),
	)
	if sent != 0 || errors.Is(err, windows.ERROR_IO_PENDING) {
		return
	}

	delete(s.pending, id)
	var errno windows.Errno
	if errors.As(err, &errno) && errno >= ipStatusBase && errno < ipStatusEnd {
		r.done <- result{}
		return
	}
	r.done <- result{err: fmt.Errorf("IcmpSendEcho2: %w", err)}
}

// completed is the APC of a request: its reply, or the lack of one, is in its buffer.
func (s *sender) completed(context, _, _ uintptr) uintptr {
	r, ok := s.pending[context]
	if !ok {
		return 0
	}
	delete(s.pending, context)

	count, _, _ := procIcmpParseReplies.Call(uintptr(unsafe.Pointer(&r.reply[0])), uintptr(len(r.reply)))
	echo := (*icmpEchoReply)(unsafe.Pointer(&r.reply[0]))
	if count == 0 || echo.Status != ipSuccess || echo.Address != r.address {
		r.done <- result{}
		return 0
	}

	r.done <- result{rtt: time.Duration(echo.RoundTripTime) * time.Millisecond, ok: true}
	return 0
}
