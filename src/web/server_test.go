package web

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/monitor"
)

type fakeMonitor struct {
	mu       sync.Mutex
	states   map[string]monitor.State
	version  uint64
	watched  map[string][]string
	replaced []string
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

func (f *fakeMonitor) Watch(viewer string, ips []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watched == nil {
		f.watched = map[string][]string{}
	}
	f.watched[viewer] = ips
}

func (f *fakeMonitor) Forget(viewer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.watched, viewer)
}

func (f *fakeMonitor) Replace(ips []string, _ []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaced = ips
}

func (f *fakeMonitor) watchedBy(viewer string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watched[viewer]
}

func serve(t *testing.T, m Monitor) (*httptest.Server, string) {
	t.Helper()

	book := &addressbook.Book{Format: addressbook.Format, Version: 1}
	server := httptest.NewUnstartedServer(nil)
	host := server.Listener.Addr().String()
	server.Config.Handler = New(book, m, "data.json").Handler([]string{host}, nil)
	server.Start()
	t.Cleanup(server.Close)

	return server, "http://" + host
}

func TestThePageAndTheBookAreServed(t *testing.T) {
	_, url := serve(t, &fakeMonitor{})

	for path, want := range map[string]string{
		"/":            "<title>Watcher Pinger</title>",
		"/app.js":      "function recount()",
		"/favicon.svg": `fill="#2563eb"`,
		"/api/book":    `"dataFile":"data.json"`,
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

// stream opens the events of a viewer and reads them line by line until the test ends or close is called.
func stream(t *testing.T, url, viewer string) (lines <-chan string, closeStream func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/events?viewer="+viewer, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out := make(chan string, 100)
	go func() {
		defer close(out)
		reader := bufio.NewReader(response.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			out <- line
		}
	}()
	closeStream = func() {
		cancel()
		_ = response.Body.Close()
	}
	t.Cleanup(closeStream)
	return out, closeStream
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if done() {
			return
		}
	}
	t.Fatalf("waited in vain for %s", what)
}

// What a page shows is watched while its stream is open, and forgotten once it closes.
func TestTheWatchedAddressesOfAPageReachTheMonitor(t *testing.T) {
	m := &fakeMonitor{}
	_, url := serve(t, m)

	watch := func(viewer string) {
		response, err := http.Post(url+"/api/watch?viewer="+viewer, "application/json",
			strings.NewReader(`["10.0.0.1","10.0.0.2"]`))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}

	watch("closed")
	if got := m.watchedBy("closed"); got != nil {
		t.Errorf("a page without a stream watches %v", got)
	}

	_, closeStream := stream(t, url, "open")
	watch("open")
	if want := []string{"10.0.0.1", "10.0.0.2"}; !reflect.DeepEqual(m.watchedBy("open"), want) {
		t.Errorf("watched %v, want %v", m.watchedBy("open"), want)
	}

	closeStream()
	waitFor(t, "the closed page to be forgotten", func() bool { return m.watchedBy("open") == nil })
}

// New data are read on request, pinged and told to the pages open; data that cannot be read change nothing.
func TestTheDataAreReadAnewOnRequest(t *testing.T) {
	m := &fakeMonitor{}
	book := &addressbook.Book{Format: addressbook.Format, Version: 1}
	server := httptest.NewUnstartedServer(nil)
	host := server.Listener.Addr().String()
	handler := New(book, m, "data.json")
	handler.Interval = 10 * time.Millisecond
	next := &addressbook.Book{Format: addressbook.Format, Version: 1, Unassigned: []addressbook.Host{
		{Name: "New", IP: "10.0.0.9", Kind: addressbook.KindCustomer},
	}}
	var failing atomic.Bool
	handler.Reload = func(context.Context) (*addressbook.Book, error) {
		if failing.Load() {
			return nil, errors.New("not the file of Watcher NMS")
		}
		return next, nil
	}
	server.Config.Handler = handler.Handler([]string{host}, nil)
	server.Start()
	t.Cleanup(server.Close)
	url := "http://" + host

	lines, _ := stream(t, url, "page")

	failing.Store(true)
	response, err := http.Post(url+"/api/reload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity || handler.book.Load() != book {
		t.Fatalf("bad data: status %d, the book replaced %v", response.StatusCode, handler.book.Load() != book)
	}

	// right after a reading the next one waits, failed or not
	failing.Store(false)
	response, err = http.Post(url+"/api/reload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests || handler.book.Load() != book {
		t.Fatalf("too soon: status %d, the book replaced %v", response.StatusCode, handler.book.Load() != book)
	}
	handler.reloadMu.Lock()
	handler.reloaded = time.Time{}
	handler.reloadMu.Unlock()

	response, err = http.Post(url+"/api/reload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || handler.book.Load() != next {
		t.Fatalf("status %d, the new book in place %v", response.StatusCode, handler.book.Load() == next)
	}
	m.mu.Lock()
	replaced := m.replaced
	m.mu.Unlock()
	if !reflect.DeepEqual(replaced, []string{"10.0.0.9"}) {
		t.Errorf("pinged %v", replaced)
	}

	timeout := time.After(5 * time.Second)
	for {
		select {
		case line := <-lines:
			if line == "event: book\n" {
				return
			}
		case <-timeout:
			t.Fatal("the page was not told of the new data")
		}
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
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/events?viewer=page", nil)
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
	server.Config.Handler = handler.Handler([]string{host}, nil)
	server.Start()
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/api/events?viewer=page", nil)
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

// Another site open in the same browser cannot post to the page: not reload, not watch.
func TestAnotherSiteCannotPost(t *testing.T) {
	book := &addressbook.Book{Format: addressbook.Format, Version: 1}
	server := httptest.NewUnstartedServer(nil)
	host := server.Listener.Addr().String()
	handler := New(book, &fakeMonitor{}, "data.json")
	var reloaded atomic.Bool
	handler.Reload = func(context.Context) (*addressbook.Book, error) {
		reloaded.Store(true)
		return book, nil
	}
	server.Config.Handler = handler.Handler([]string{host}, nil)
	server.Start()
	t.Cleanup(server.Close)
	url := "http://" + host

	for path, headers := range map[string]map[string]string{
		"/api/reload":            {"Sec-Fetch-Site": "cross-site"},
		"/api/watch?viewer=page": {"Sec-Fetch-Site": "same-site"},
		"/api/reload?origin":     {"Origin": "https://evil.example"},
	} {
		request, _ := http.NewRequest(http.MethodPost, url+path, strings.NewReader("[]"))
		request.Header.Set("Content-Type", "text/plain")
		for k, v := range headers {
			request.Header.Set(k, v)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("%s with %v: status %d", path, headers, response.StatusCode)
		}
	}
	if reloaded.Load() {
		t.Error("read anew for another site")
	}

	// the page itself may
	request, _ := http.NewRequest(http.MethodPost, url+"/api/reload", nil)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !reloaded.Load() {
		t.Errorf("the page itself: status %d", response.StatusCode)
	}
}

// No other site shows the page in a frame of its own.
func TestThePageIsNotFramed(t *testing.T) {
	_, url := serve(t, &fakeMonitor{})

	response, err := http.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.Header.Get("Content-Security-Policy") != "frame-ancestors 'none'" ||
		response.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers %v", response.Header)
	}
}

// A viewer that is no ID of a page, or one with too many streams, gets none.
func TestStreamsAreBounded(t *testing.T) {
	_, url := serve(t, &fakeMonitor{})

	for viewer, want := range map[string]int{
		"":                      http.StatusBadRequest,
		strings.Repeat("a", 65): http.StatusBadRequest,
		"page%0A":               http.StatusBadRequest,
		"../../etc":             http.StatusBadRequest,
	} {
		response, err := http.Get(url + "/api/events?viewer=" + viewer)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("viewer %q: status %d, want %d", viewer, response.StatusCode, want)
		}
	}

	for range maxStreamsOfViewer {
		stream(t, url, "page")
	}
	response, err := http.Get(url + "/api/events?viewer=page")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Errorf("one stream too many: status %d", response.StatusCode)
	}
}

// Only the browser the pinger opened gets in: the key in the address becomes a cookie of its port and leaves the
// address, and a browser without it is told where to open the page from, in its language.
func TestTheKeyLetsInOnlyTheBrowserThePingerOpened(t *testing.T) {
	const key = "the-key-of-the-user-0123456789abcdef"
	book := &addressbook.Book{Format: addressbook.Format, Version: 1}
	server := httptest.NewUnstartedServer(nil)
	host := server.Listener.Addr().String()
	handler := New(book, &fakeMonitor{}, "data.json")
	handler.Key = key
	server.Config.Handler = handler.Handler([]string{host}, nil)
	server.Start()
	t.Cleanup(server.Close)
	_, port, _ := net.SplitHostPort(host)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, language string, cookie *http.Cookie) (*http.Response, string) {
		t.Helper()
		request, _ := http.NewRequest(http.MethodGet, "http://"+host+path, nil)
		if language != "" {
			request.Header.Set("Accept-Language", language)
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body := new(strings.Builder)
		_, _ = bufio.NewReader(response.Body).WriteTo(body)
		_ = response.Body.Close()
		return response, body.String()
	}

	for path, want := range map[string]string{
		"/":                      "Open the page from Watcher Pinger",
		"/?key=not-the-key":      "Open the page from Watcher Pinger",
		"/api/book":              `"error":"no key`,
		"/api/events?viewer=abc": `"error":"no key`,
	} {
		if response, body := get(path, "en-GB", nil); response.StatusCode != http.StatusForbidden || !strings.Contains(body, want) {
			t.Errorf("%s: %d %.120q", path, response.StatusCode, body)
		}
	}
	if response, body := get("/", "cs-CZ,cs;q=0.9", nil); response.StatusCode != http.StatusForbidden ||
		!strings.Contains(body, "Otevřete stránku z Watcher Pingeru") {
		t.Errorf("in Czech: %d %.120q", response.StatusCode, body)
	}
	if response, _ := get("/favicon.svg", "", nil); response.StatusCode != http.StatusOK {
		t.Errorf("the icon of the page without a key: %d", response.StatusCode)
	}

	response, _ := get("/?tab=customers&key="+key, "", nil)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/?tab=customers" {
		t.Fatalf("the key in the address: %d to %q", response.StatusCode, response.Header.Get("Location"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != KeyCookie+port || !cookies[0].HttpOnly ||
		cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies %+v", cookies)
	}

	if response, body := get("/api/book", "", cookies[0]); response.StatusCode != http.StatusOK ||
		!strings.Contains(body, `"dataFile":"data.json"`) {
		t.Errorf("with the cookie: %d %.80q", response.StatusCode, body)
	}
	other := &http.Cookie{Name: KeyCookie + "1", Value: key}
	if response, _ := get("/api/book", "", other); response.StatusCode != http.StatusForbidden {
		t.Errorf("the cookie of another port: %d", response.StatusCode)
	}
}
