package nms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const network = `{"format":"watcher-pinger","version":1,"generated":"2026-11-19T21:00:00+01:00",` +
	`"accessPoints":[{"id":"r","name":"Root","parentId":null,"hosts":[` +
	`{"name":"Router","ip":"10.0.0.1","kind":"routeros-device","manufacturer":null,"contact":false}]}],"unassigned":[]}`

func nmsAnswering(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path || r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTheNetworkIsFetchedWithTheKey(t *testing.T) {
	server := nmsAnswering(t, http.StatusOK, network)

	book, body, err := New(server.URL+"/", "key").Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(book.IPs()) != 1 || string(body) != network {
		t.Errorf("addresses %v, body %q", book.IPs(), body)
	}
}

// What Watcher NMS refuses, or hands out other than the network, is an error that says where it came from.
func TestWhatIsNotTheNetworkIsAnError(t *testing.T) {
	for name, tc := range map[string]struct {
		token, body string
		status      int
		want        string
	}{
		"another key":    {"other", network, http.StatusOK, "403 Forbidden"},
		"a server error": {"key", "oops", http.StatusInternalServerError, "500 Internal Server Error"},
		"another file":   {"key", `{"format":"something else"}`, http.StatusOK, "handed out no network"},
	} {
		server := nmsAnswering(t, tc.status, tc.body)
		_, _, err := New(server.URL, tc.token).Fetch(context.Background())
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), server.URL) {
			t.Errorf("%s: %v, want %q and the address", name, err, tc.want)
		}
	}
}

// A redirect is not followed, the key would go with it.
func TestARedirectIsNotFollowed(t *testing.T) {
	followed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	if _, _, err := New(server.URL, "key").Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "302") {
		t.Errorf("err = %v", err)
	}
	if followed {
		t.Error("the redirect was followed")
	}
}
