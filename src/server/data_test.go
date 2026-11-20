package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"watcher-pinger/src/nms"
)

const fetched = `{"format":"watcher-pinger","version":1,"generated":"2026-11-19T21:00:00+01:00",` +
	`"accessPoints":[{"id":"r","name":"Root","parentId":null,"hosts":[` +
	`{"name":"Router","ip":"10.0.0.1","kind":"routeros-device","manufacturer":null,"contact":false}]}],"unassigned":[]}`

// The network fetched is kept, and a start with Watcher NMS out of reach pings the copy kept last time.
func TestTheLastNetworkFetchedIsKeptForAStartWithoutNMS(t *testing.T) {
	var down atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() || r.URL.Path != nms.Path {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(fetched))
	}))
	t.Cleanup(server.Close)
	file := filepath.Join(t.TempDir(), "watcher-pinger.json")
	data := newSource(Config{
		WatcherNMSURL: server.URL, WatcherNMSToken: "key", DataFile: file, WatcherNMSInterval: time.Hour,
	})

	book, err := data.first(context.Background())
	if err != nil || len(book.IPs()) != 1 {
		t.Fatalf("%v, %v", err, book)
	}
	if kept, err := os.ReadFile(file); err != nil || string(kept) != fetched {
		t.Fatalf("kept %q, %v", kept, err)
	}

	down.Store(true)
	if _, err := data.load(context.Background()); err == nil {
		t.Error("a Watcher NMS that is down handed out a network")
	}
	book, err = data.first(context.Background())
	if err != nil || len(book.IPs()) != 1 {
		t.Errorf("the start from the copy: %v, %v", err, book)
	}
	if data.label() != server.URL {
		t.Errorf("the page says the data come from %q", data.label())
	}
}

// Without a copy, a start with Watcher NMS out of reach has nothing to ping and says so.
func TestAStartWithoutNMSAndWithoutACopyFails(t *testing.T) {
	data := newSource(Config{
		WatcherNMSURL: "http://127.0.0.1:1", WatcherNMSToken: "key", DataFile: filepath.Join(t.TempDir(), "none.json"),
	})

	if _, err := data.first(context.Background()); err == nil {
		t.Error("started with nothing")
	}
}
