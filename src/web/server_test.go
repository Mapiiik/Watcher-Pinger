package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/monitor"
)

type fakeMonitor struct {
	mu      sync.Mutex
	states  map[string]monitor.State
	version uint64
	watched []string
}

func (f *fakeMonitor) Snapshot() (map[string]monitor.State, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states, f.version
}

func (f *fakeMonitor) Since(version uint64) (map[string]monitor.State, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version == f.version {
		return nil, f.version
	}
	return f.states, f.version
}

func (f *fakeMonitor) Watch(ips []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = ips
}

func serve(t *testing.T, m Monitor) (*httptest.Server, string) {
	t.Helper()

	book := &addressbook.Book{Format: addressbook.Format, Version: 1}
	server := httptest.NewUnstartedServer(nil)
	host := server.Listener.Addr().String()
	server.Config.Handler = New(book, m, "data.json").Handler(host)
	server.Start()
	t.Cleanup(server.Close)

	return server, "http://" + host
}

func TestThePageAndTheBookAreServed(t *testing.T) {
	_, url := serve(t, &fakeMonitor{})

	for path, want := range map[string]string{
		"/":         "<title>Watcher Pinger</title>",
		"/app.js":   "function recount()",
		"/api/book": `"dataFile":"data.json"`,
	} {
		response, err := http.Get(url + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		body := new(strings.Builder)
		_, _ = bufio.NewReader(response.Body).WriteTo(body)
		_ = response.Body.Close()

		if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), want) {
			t.Errorf("%s: %d, want %q in %.200q", path, response.StatusCode, want, body.String())
		}
	}
}

// A page of another site that had its name point to the loopback is not answered.
func TestAnotherHostIsRefused(t *testing.T) {
	_, url := serve(t, &fakeMonitor{})

	request, _ := http.NewRequest(http.MethodGet, url+"/api/book", nil)
	request.Host = "evil.example.com"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", response.StatusCode)
	}
}

func TestTheWatchedAddressesReachTheMonitor(t *testing.T) {
	m := &fakeMonitor{}
	_, url := serve(t, m)

	response, err := http.Post(url+"/api/watch", "application/json", strings.NewReader(`["10.0.0.1","10.0.0.2"]`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if want := []string{"10.0.0.1", "10.0.0.2"}; !reflect.DeepEqual(m.watched, want) {
		t.Errorf("watched %v, want %v", m.watched, want)
	}
}

func TestTheStatesAreStreamedEveryOneFirst(t *testing.T) {
	reply := time.Unix(1_800_000_000, 0)
	m := &fakeMonitor{states: map[string]monitor.State{
		"10.0.0.1": {
			Status: monitor.Up, RTT: 1500 * time.Microsecond, LastReply: reply, Since: reply,
			Sent: 4, Received: 3, RTTAvg: 2250 * time.Microsecond, RTTMax: 4 * time.Millisecond,
		},
	}}
	_, url := serve(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if want := `data: {"10.0.0.1":[1,1.5,0,1800000000,1800000000,4,3,2.25,4]}` + "\n"; line != want {
		t.Errorf("got %q, want %q", line, want)
	}
}

// Without a change the page still hears from the pinger, so that it can tell a quiet network from a pinger that is
// gone.
func TestAQuietStreamStillBeats(t *testing.T) {
	book := &addressbook.Book{Format: addressbook.Format, Version: 1}
	server := httptest.NewUnstartedServer(nil)
	host := server.Listener.Addr().String()
	handler := New(book, &fakeMonitor{}, "data.json")
	handler.Interval = 10 * time.Millisecond
	handler.Heartbeat = 50 * time.Millisecond
	server.Config.Handler = handler.Handler(host)
	server.Start()
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/api/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	reader := bufio.NewReader(response.Body)
	start := time.Now()
	for beats := 0; beats < 3; {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "data: {}\n" {
			beats++
		}
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("three beats in %v, want them a heartbeat apart", elapsed)
	}
}
