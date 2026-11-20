package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/web"
)

// portTries is how many ports after the usual one a pinger tries where other users of the computer run theirs.
const portTries = 20

// pageKey is the key the page of this user opens with. It is made at the first start and kept in the folder of the
// user's settings, which nobody else reads, so that a bookmark of the page works after a start again too.
func pageKey() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "Watcher Pinger")
	path := filepath.Join(dir, "page.key")
	if kept, err := os.ReadFile(path); err == nil {
		if key := strings.TrimSpace(string(kept)); len(key) >= 32 {
			return key, nil
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

// listenFor takes the address. Where it is taken, a pinger of this user may be running there already, told by the key,
// and its address is returned to open its page; otherwise, another user's pinger say, the next free port is taken,
// where tryNext lets it.
func listenFor(address, key string, tryNext bool) (net.Listener, string, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, "", err
	}
	tries := 1
	if tryNext && port != 0 {
		tries = portTries
	}

	var first error
	for i := range tries {
		at := net.JoinHostPort(host, strconv.Itoa(port+i))
		listener, err := net.Listen("tcp", at)
		if err == nil {
			return listener, "", nil
		}
		if first == nil {
			first = err
		}
		if base := "http://" + at + "/"; isOwnPinger(base, key) {
			return nil, base, nil
		}
	}
	return nil, "", first
}

// isOwnPinger tells whether a Watcher Pinger of this user answers at the address: one of another user does not take
// the key.
func isOwnPinger(base, key string) bool {
	address, err := url.Parse(base)
	if err != nil {
		return false
	}
	request, err := http.NewRequest(http.MethodGet, base+"api/book", nil)
	if err != nil {
		return false
	}
	request.AddCookie(&http.Cookie{Name: web.KeyCookie + address.Port(), Value: key})

	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()

	var answer struct {
		Book struct {
			Format string `json:"format"`
		} `json:"book"`
	}
	return response.StatusCode == http.StatusOK &&
		json.NewDecoder(response.Body).Decode(&answer) == nil &&
		answer.Book.Format == addressbook.Format
}

// withKey is the address of the page that lets the browser in.
func withKey(base, key string) string {
	return base + "?key=" + url.QueryEscape(key)
}
