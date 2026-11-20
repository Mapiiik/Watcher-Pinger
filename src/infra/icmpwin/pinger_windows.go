//go:build windows

// Package icmpwin pings IPv4 and IPv6 through the ICMP helper of Windows, which needs no administrator, unlike a raw
// socket.
//
// Requests go out from a few threads of the pinger's own, each taking the replies to its requests as APCs while it
// waits. A thread waiting in IcmpSendEcho for each request costs the processor ten times as much once there are a
// couple of hundred of them, as every one is woken whenever any request is done. Sending a request to another host
// takes a couple of milliseconds of the processor, so a single thread would hold up the replies behind a long round.
package icmpwin

import (
	"bytes"
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

	procIcmp6CreateFile   = iphlpapi.NewProc("Icmp6CreateFile")
	procIcmp6SendEcho2    = iphlpapi.NewProc("Icmp6SendEcho2")
	procIcmp6ParseReplies = iphlpapi.NewProc("Icmp6ParseReplies")

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

// ICMPV6_ECHO_REPLY is read by offsets: its address is an IPV6_ADDRESS_EX, which is packed (port, flow, address,
// scope), and the status and round trip follow at the next multiple of four.
const (
	icmp6ReplyAddress = 6
	icmp6ReplyStatus  = 28
	icmp6ReplyRTT     = 32
)

const (
	// ipSuccess is the status of an echo reply from the address asked.
	ipSuccess = 0

	// ipReqTimedOut is IP_REQ_TIMED_OUT, no reply in time.
	ipReqTimedOut = 11010

	// ipStatusBase is where the IP_STATUS codes start. With most of them the request went out and nothing, or
	// something other than the address asked, answered it; localFailures did not let it out at all.
	ipStatusBase = 11000
	ipStatusEnd  = 12000

	// maxSenders bounds the threads sending requests, half the processors otherwise.
	maxSenders = 8

	// spillAt is how many requests may wait for the first thread before the others take some.
	spillAt = 16
)

var (
	payload = []byte("watcher-pinger")

	// replySize leaves room for the reply of either family, its data, an ICMP error message and the status block of
	// an asynchronous request.
	replySize = int(unsafe.Sizeof(icmpEchoReply{})) + len(payload) + 8 + 32

	errClosed = errors.New("the pinger is closed")
)

// localFailures are the IP_STATUS codes of a request that never left the machine: a Windows without a network
// answers IP_GENERAL_FAILURE, say. They are errors, so that the pinger tells "cannot ping" rather than every address
// down.
var localFailures = map[uint32]string{
	11001: "IP_BUF_TOO_SMALL",
	11006: "IP_NO_RESOURCES",
	11007: "IP_BAD_OPTION",
	11008: "IP_HW_ERROR",
	11011: "IP_BAD_REQ",
	11018: "IP_BAD_DESTINATION",
	11050: "IP_GENERAL_FAILURE",
}

// statusResult is the result of a request the helper gave the status of: an answer, none, or an error where the
// request never left.
func statusResult(status uint32, rtt time.Duration) result {
	if status == ipSuccess {
		return result{rtt: rtt, ok: true}
	}
	if name, local := localFailures[status]; local {
		return result{err: fmt.Errorf("the ICMP helper: %s (%d)", name, status)}
	}
	return result{}
}

type result struct {
	rtt time.Duration
	ok  bool
	err error
}

type request struct {
	// an IPv4 address as the helper takes it, or an IPv6 one with the addresses of both ends
	address     uint32
	v6          bool
	address6    [16]byte
	source      *windows.RawSockaddrInet6
	destination *windows.RawSockaddrInet6

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

// sender is one thread sending requests over handles of its own and taking the replies to them.
type sender struct {
	handle uintptr
	// handle6 is 0 on a Windows without IPv6, err6 then says why
	handle6  uintptr
	err6     error
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

	// without IPv6 the sender still pings IPv4
	handle6, _, err6 := procIcmp6CreateFile.Call()
	if handle6 == uintptr(windows.InvalidHandle) {
		handle6, err6 = 0, fmt.Errorf("Icmp6CreateFile: %w", err6)
	} else {
		err6 = nil
	}

	s := &sender{
		handle:  handle,
		handle6: handle6,
		err6:    err6,
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
		if s.handle6 != 0 {
			if ok, _, err := procIcmpCloseHandle.Call(s.handle6); ok == 0 {
				failed = fmt.Errorf("IcmpCloseHandle: %w", err)
			}
		}
	}
	return failed
}

// Ping sends one echo request and waits for its reply. Without one in time, or with one from elsewhere, ok is false
// and there is no error: an error means the request did not go out. The helper measures in whole milliseconds.
func (p *Pinger) Ping(ip net.IP, timeout time.Duration) (time.Duration, bool, error) {
	milliseconds := uint32(timeout / time.Millisecond)
	if milliseconds == 0 {
		milliseconds = 1
	}
	r := &request{
		timeout: milliseconds,
		reply:   make([]byte, replySize),
		done:    make(chan result, 1),
	}
	switch {
	case ip.To4() != nil:
		r.address = binary.LittleEndian.Uint32(ip.To4())
	case len(ip) == net.IPv6len:
		r.v6 = true
		copy(r.address6[:], ip)
		r.source = &windows.RawSockaddrInet6{Family: windows.AF_INET6}
		r.destination = &windows.RawSockaddrInet6{Family: windows.AF_INET6, Addr: r.address6}
	default:
		return 0, false, errors.New("not an IP address")
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
	if r.v6 && s.handle6 == 0 {
		r.done <- result{err: s.err6}
		return
	}

	id := s.nextID
	s.nextID++
	s.pending[id] = r

	var sent uintptr
	var err error
	if r.v6 {
		sent, _, err = procIcmp6SendEcho2.Call(
			s.handle6,
			0,
			s.callback,
			id,
			uintptr(unsafe.Pointer(r.source)),
			uintptr(unsafe.Pointer(r.destination)),
			uintptr(unsafe.Pointer(&payload[0])),
			uintptr(len(payload)),
			0,
			uintptr(unsafe.Pointer(&r.reply[0])),
			uintptr(len(r.reply)),
			uintptr(r.timeout),
		)
	} else {
		sent, _, err = procIcmpSendEcho2.Call(
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
	}
	if sent != 0 || errors.Is(err, windows.ERROR_IO_PENDING) {
		return
	}

	delete(s.pending, id)
	var errno windows.Errno
	if errors.As(err, &errno) && errno >= ipStatusBase && errno < ipStatusEnd {
		r.done <- statusResult(uint32(errno), 0)
		return
	}
	if r.v6 {
		r.done <- result{err: fmt.Errorf("Icmp6SendEcho2: %w", err)}
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

	if r.v6 {
		count, _, err := procIcmp6ParseReplies.Call(uintptr(unsafe.Pointer(&r.reply[0])), uintptr(len(r.reply)))
		status := replyStatus(count, err, binary.LittleEndian.Uint32(r.reply[icmp6ReplyStatus:]))
		from := r.reply[icmp6ReplyAddress : icmp6ReplyAddress+net.IPv6len]
		if status == ipSuccess && !bytes.Equal(from, r.address6[:]) {
			r.done <- result{}
			return 0
		}
		rtt := binary.LittleEndian.Uint32(r.reply[icmp6ReplyRTT:])
		r.done <- statusResult(status, time.Duration(rtt)*time.Millisecond)
		return 0
	}

	count, _, err := procIcmpParseReplies.Call(uintptr(unsafe.Pointer(&r.reply[0])), uintptr(len(r.reply)))
	echo := (*icmpEchoReply)(unsafe.Pointer(&r.reply[0]))
	status := replyStatus(count, err, echo.Status)
	if status == ipSuccess && echo.Address != r.address {
		r.done <- result{}
		return 0
	}
	r.done <- statusResult(status, time.Duration(echo.RoundTripTime)*time.Millisecond)
	return 0
}

// replyStatus is the status of a reply: the one in the buffer, or without a reply parsed the one the parser left as
// the last error, and a timeout where it left none.
func replyStatus(count uintptr, err error, inBuffer uint32) uint32 {
	if count > 0 {
		return inBuffer
	}
	var errno windows.Errno
	if errors.As(err, &errno) && errno >= ipStatusBase && errno < ipStatusEnd {
		return uint32(errno)
	}
	if inBuffer != ipSuccess {
		return inBuffer
	}
	return ipReqTimedOut
}
