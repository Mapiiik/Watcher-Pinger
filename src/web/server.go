// Package web serves the page of the pinger and what it shows: the tree read from the file and the states of the
// addresses as they change.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/monitor"
)

//go:embed static
var static embed.FS

// reloadGap is the least time between two readings asked for on a page, which go to Watcher NMS on a server.
const reloadGap = 5 * time.Second

var errTooSoon = errors.New("the data were read anew a moment ago, try again in a few seconds")

// Monitor is what the page needs of the monitor.
type Monitor interface {
	Snapshot() (map[string]monitor.State, uint64)
	Since(version uint64) (map[string]monitor.State, uint64)
	Watch(viewer string, ips []string)
	Forget(viewer string)
	Replace(ips []string, devices []string)
}

// Server answers the page.
type Server struct {
	book     atomic.Pointer[addressbook.Book]
	books    atomic.Uint64
	monitor  Monitor
	dataFile string

	// streams counts the streams open for each viewer, a page open
	streamsMu sync.Mutex
	streams   map[string]int
	reloadMu  sync.Mutex
	reloaded  time.Time

	// Interval is how often the page is sent what changed.
	Interval time.Duration
	// Heartbeat is how long the page goes without a message at most, so that it can tell a quiet network from a
	// pinger that is gone.
	Heartbeat time.Duration
	// Identity tells the page who is signed in and where they sign out, nil where nobody signs in.
	Identity func(r *http.Request) (user, signOut string)
	// Who is the user name of a request for the log, nil where nobody signs in.
	Who func(r *http.Request) string
	// Reload reads the data anew when the page asks for it, nil where it cannot.
	Reload func(ctx context.Context) (*addressbook.Book, error)
	// Hosted tells the page it is served by a server rather than by the pinger of the one looking at it.
	Hosted bool
}

// New serves the given book and the states its monitor keeps.
func New(book *addressbook.Book, monitor Monitor, dataFile string) *Server {
	s := &Server{
		monitor:   monitor,
		dataFile:  dataFile,
		streams:   make(map[string]int),
		Interval:  250 * time.Millisecond,
		Heartbeat: 2 * time.Second,
	}
	s.book.Store(book)
	return s
}

// SetBook makes the given book the one pinged and shown, and tells the pages open to read it.
func (s *Server) SetBook(book *addressbook.Book) {
	s.book.Store(book)
	s.monitor.Replace(book.IPs(), book.DeviceIPs())
	s.books.Add(1)
}

// Handler answers requests for the given hosts only, any host where none is given. A pinger on the loopback would
// otherwise answer a page of another site that had its name point there. The guard, where there is one, stands
// between the check of the host and the page.
func (s *Server) Handler(hosts []string, guard func(http.Handler) http.Handler) http.Handler {
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(files))
	mux.HandleFunc("GET /api/book", s.handleBook)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/watch", s.handleWatch)
	mux.HandleFunc("POST /api/reload", s.handleReload)

	var page http.Handler = mux
	if guard != nil {
		page = guard(mux)
	}

	// Another site open in the same browser must not reload or watch through the page: a POST has to come from the
	// page itself (Sec-Fetch-Site, or Origin against Host), a sister domain included.
	crossOrigin := http.NewCrossOriginProtection()
	crossOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Warn("request refused", "user", s.who(r), "client", clientIP(r), "path", r.URL.Path,
			"reason", "from another site", "origin", r.Header.Get("Origin"))
		http.Error(w, "a request from another site", http.StatusForbidden)
	}))
	page = crossOrigin.Handler(page)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(hosts) > 0 && !slices.Contains(hosts, r.Host) {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		header := w.Header()
		header.Set("Cache-Control", "no-store")
		// no other site shows the page in a frame of its own, to have it clicked unseen
		header.Set("Content-Security-Policy", "frame-ancestors 'none'")
		header.Set("X-Frame-Options", "DENY")
		header.Set("X-Content-Type-Options", "nosniff")
		page.ServeHTTP(w, r)
	})
}

// handleSession tells the page who is signed in, so that it can show them and offer to sign out. The page also asks
// it once the states stop coming: a session run out answers 401, a pinger gone does not answer at all.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	var session struct {
		User    string `json:"user,omitempty"`
		SignOut string `json:"signOut,omitempty"`
	}
	if s.Identity != nil {
		session.User, session.SignOut = s.Identity(r)
	}
	writeJSON(w, session)
}

func (s *Server) handleBook(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, struct {
		DataFile  string            `json:"dataFile"`
		Book      *addressbook.Book `json:"book"`
		CanReload bool              `json:"canReload"`
		Hosted    bool              `json:"hosted"`
	}{s.dataFile, s.book.Load(), s.Reload != nil, s.Hosted})
}

// handleReload reads the data anew, not twice within reloadGap. Data that cannot be read change nothing, and the page
// is told why. Who asked is logged.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if s.Reload == nil {
		http.Error(w, "the data cannot be read anew here", http.StatusNotFound)
		return
	}
	book, err := s.reload(r.Context(), reloadGap)
	if err != nil {
		slog.Warn("data not read anew", "user", s.who(r), "client", clientIP(r), "error", err)
		status := http.StatusUnprocessableEntity
		if errors.Is(err, errTooSoon) {
			status = http.StatusTooManyRequests
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	slog.Info("data read anew", "user", s.who(r), "client", clientIP(r), "generated", book.Generated)
	writeJSON(w, map[string]any{"generated": book.Generated})
}

// ReloadNow reads the data anew and makes them the ones pinged and shown, one reading at a time. Data that cannot be
// read change nothing.
func (s *Server) ReloadNow(ctx context.Context) (*addressbook.Book, error) {
	return s.reload(ctx, 0)
}

// reload is ReloadNow that refuses when the last reading, whether it worked or not, is less than gap ago.
func (s *Server) reload(ctx context.Context, gap time.Duration) (*addressbook.Book, error) {
	if s.Reload == nil {
		return nil, errors.New("the data cannot be read anew here")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	if gap > 0 && time.Since(s.reloaded) < gap {
		return nil, errTooSoon
	}
	book, err := s.Reload(ctx)
	s.reloaded = time.Now()
	if err != nil {
		return nil, err
	}
	s.SetBook(book)
	return book, nil
}

// who is the user of the request for the log, empty where nobody signs in.
func (s *Server) who(r *http.Request) string {
	if s.Who == nil {
		return ""
	}
	return s.Who(r)
}

// clientIP is the address of who asks, the one from the PROXY header behind a proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Book is the book pinged and shown now.
func (s *Server) Book() *addressbook.Book {
	return s.book.Load()
}

// handleEvents sends every state first and then, at each interval, the ones that changed, or nothing at all once a
// heartbeat has passed without a change. New data are told as an event of their own, for the page to read them. The
// stream belongs to a viewer, whose watched addresses are forgotten once its last stream is closed.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")

	viewer := r.URL.Query().Get("viewer")
	s.open(viewer)
	defer s.close(viewer)

	states, version := s.monitor.Snapshot()
	books := s.books.Load()
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	var sent time.Time

	for {
		if b := s.books.Load(); b != books {
			books = b
			if _, err := fmt.Fprint(w, "event: book\ndata: {}\n\n"); err != nil {
				return
			}
		}
		if len(states) > 0 || time.Since(sent) >= s.Heartbeat {
			payload, err := json.Marshal(encode(states))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			sent = time.Now()
		}
		flusher.Flush()

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		states, version = s.monitor.Since(version)
	}
}

// open counts a stream of the viewer.
func (s *Server) open(viewer string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	s.streams[viewer]++
}

// close counts a stream of the viewer off, and forgets what it watched with its last one.
func (s *Server) close(viewer string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	s.streams[viewer]--
	if s.streams[viewer] <= 0 {
		delete(s.streams, viewer)
		s.monitor.Forget(viewer)
	}
}

// handleWatch takes what a viewer shows. A viewer without a stream open is not believed, as nothing would ever forget
// what it watches; the page sends it again once its stream opens.
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	var ips []string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ips); err != nil {
		http.Error(w, "a list of addresses expected", http.StatusBadRequest)
		return
	}
	viewer := r.URL.Query().Get("viewer")

	s.streamsMu.Lock()
	open := s.streams[viewer] > 0
	if open {
		s.monitor.Watch(viewer, ips)
	}
	s.streamsMu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// encode writes a state as [status, round trip in ms, lost in a row, last reply, status since, sent, received,
// average round trip in ms, longest round trip in ms], the last four over the last pings, times in Unix seconds and 0
// for never. A list rather than an object keeps a full list of thousands of addresses small.
func encode(states map[string]monitor.State) map[string][9]any {
	encoded := make(map[string][9]any, len(states))
	for ip, state := range states {
		encoded[ip] = [9]any{
			state.Status,
			milliseconds(state.RTT),
			state.Lost,
			unix(state.LastReply),
			unix(state.Since),
			state.Sent,
			state.Received,
			milliseconds(state.RTTAvg),
			milliseconds(state.RTTMax),
		}
	}
	return encoded
}

func milliseconds(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
