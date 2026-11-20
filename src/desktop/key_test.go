package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/web"
)

// The key is made once and kept where only the user reads it, so that a bookmark works after a start again too.
func TestThePageKeyIsKeptForTheUser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)

	first, err := pageKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := pageKey()
	if err != nil || second != first || len(first) < 43 {
		t.Fatalf("%q then %q, %v", first, second, err)
	}

	config, _ := os.UserConfigDir()
	info, err := os.Stat(filepath.Join(config, "Watcher Pinger", "page.key"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("the key can be read by others: %v", info.Mode())
	}
}

// pingerOf serves what a pinger with the key answers: the book to its own browser, a refusal to anybody else.
func pingerOf(t *testing.T, key string) (address string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(web.KeyCookie + port); err != nil || c.Value != key {
			http.Error(w, "no key", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"book": map[string]string{"format": addressbook.Format}})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().String()
}

// A pinger of the same user is found by its key, and its page opened rather than a second one started.
func TestThePingerOfTheUserIsFound(t *testing.T) {
	address := pingerOf(t, "the-key")

	listener, running, err := listenFor(address, "the-key", true)
	if listener != nil {
		_ = listener.Close()
	}
	if err != nil || listener != nil || running != "http://"+address+"/" {
		t.Errorf("listener %v, running %q, err %v", listener, running, err)
	}
}

// The pinger of another user of the computer is passed over: the next free port is taken, where that is allowed.
func TestThePingerOfAnotherUserIsPassedOver(t *testing.T) {
	address := pingerOf(t, "their-key")
	_, port, _ := net.SplitHostPort(address)

	listener, running, err := listenFor(address, "my-key", true)
	if err != nil || listener == nil || running != "" {
		t.Fatalf("listener %v, running %q, err %v", listener, running, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, taken, _ := net.SplitHostPort(listener.Addr().String())
	if mine, _ := strconv.Atoi(taken); strconv.Itoa(mine) == port {
		t.Errorf("took the port of the other pinger, %s", port)
	}

	if l, running, err := listenFor(address, "my-key", false); err == nil || l != nil || running != "" {
		t.Errorf("an address given took another port: %v %q %v", l, running, err)
	}
}

func TestTheAddressCarriesTheKey(t *testing.T) {
	if got := withKey("http://127.0.0.1:47380/", "a+b/c"); got != "http://127.0.0.1:47380/?key=a%2Bb%2Fc" {
		t.Errorf("%q", got)
	}
}
