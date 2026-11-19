// Package web serves the page of the pinger and what it shows: the tree read from the file and the states of the
// addresses as they change.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/monitor"
)

//go:embed static
var static embed.FS

// Monitor is what the page needs of the monitor.
type Monitor interface {
	Snapshot() (map[string]monitor.State, uint64)
	Since(version uint64) (map[string]monitor.State, uint64)
	Watch(ips []string)
}

// Server answers the page.
type Server struct {
	book     *addressbook.Book
	monitor  Monitor
	dataFile string
	// Interval is how often the page is sent what changed.
	Interval time.Duration
	// Heartbeat is how long the page goes without a message at most, so that it can tell a quiet network from a
	// pinger that is gone.
	Heartbeat time.Duration
}

// New serves the given book and the states its monitor keeps.
func New(book *addressbook.Book, monitor Monitor, dataFile string) *Server {
	return &Server{
		book:      book,
		monitor:   monitor,
		dataFile:  dataFile,
		Interval:  250 * time.Millisecond,
		Heartbeat: 2 * time.Second,
	}
}

// Handler answers requests for the given host only. The server listens on the loopback, and a page of another site
// that had its name point there would otherwise be answered too.
func (s *Server) Handler(host string) http.Handler {
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(files))
	mux.HandleFunc("GET /api/book", s.handleBook)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/watch", s.handleWatch)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleBook(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, struct {
		DataFile string            `json:"dataFile"`
		Book     *addressbook.Book `json:"book"`
	}{s.dataFile, s.book})
}

// handleEvents sends every state first and then, at each interval, the ones that changed, or nothing at all once a
// heartbeat has passed without a change.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")

	states, version := s.monitor.Snapshot()
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	var sent time.Time

	for {
		if len(states) > 0 || time.Since(sent) >= s.Heartbeat {
			payload, err := json.Marshal(encode(states))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
			sent = time.Now()
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		states, version = s.monitor.Since(version)
	}
}

func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	var ips []string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ips); err != nil {
		http.Error(w, "a list of addresses expected", http.StatusBadRequest)
		return
	}
	s.monitor.Watch(ips)
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
