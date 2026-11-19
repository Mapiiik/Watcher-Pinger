// Watcher Pinger pings the network Watcher NMS knows and shows it as a tree in the browser. It runs from a file
// handed out by Watcher NMS, so that it works in a part of the network cut off from the rest.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"watcher-pinger/src/addressbook"
	"watcher-pinger/src/infra/icmpwin"
	"watcher-pinger/src/monitor"
	"watcher-pinger/src/web"
)

const (
	// dataFileName is the file Watcher NMS hands out, looked for next to the program.
	dataFileName = "watcher-pinger.json"

	// defaultListen stays the same between runs, so that an open page reconnects to a pinger started again, and
	// the browser keeps what was expanded, which it keeps per port.
	defaultListen = "127.0.0.1:47380"
)

// version is set by the build, from the tag of the commit.
var version = "dev"

func main() {
	data := flag.String("data", defaultDataFile(), "the file handed out by Watcher NMS")
	listen := flag.String("listen", defaultListen, "where the page is served")
	noBrowser := flag.Bool("no-browser", false, "do not open the page in the browser")
	timeout := flag.Duration("timeout", monitor.DefaultOptions.Timeout, "how long to wait for a reply")
	watchedInterval := flag.Duration("watched-interval", monitor.DefaultOptions.WatchedInterval,
		"how often the addresses shown on the page are pinged")
	deviceInterval := flag.Duration("device-interval", monitor.DefaultOptions.DeviceInterval,
		"how often the other devices of the network are pinged")
	idleInterval := flag.Duration("idle-interval", monitor.DefaultOptions.IdleInterval,
		"how often the other customers are pinged")
	showVersion := flag.Bool("version", false, "tell the version and stop")
	flag.Parse()

	if *showVersion {
		fmt.Println("Watcher Pinger", version)
		return
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		// started a second time, it shows the page of the one running
		if url := "http://" + *listen + "/"; isPinger(url) {
			fmt.Printf("Watcher Pinger is running already, its page is at %s\n", url)
			if !*noBrowser {
				_ = openBrowser(url)
			}
			return
		}
		fail("Cannot listen on %s: %v", *listen, err)
	}

	book, err := addressbook.Load(*data)
	if err != nil {
		fail("Cannot read %s: %v", *data, err)
	}

	pinger, err := icmpwin.Open()
	if err != nil {
		fail("Cannot ping: %v", err)
	}
	defer func() { _ = pinger.Close() }()

	options := monitor.DefaultOptions
	options.Timeout = *timeout
	options.WatchedInterval = *watchedInterval
	options.DeviceInterval = *deviceInterval
	options.IdleInterval = *idleInterval
	pings := monitor.New(pinger, book.IPs(), book.DeviceIPs(), options)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go pings.Run(ctx)

	host := listener.Addr().String()
	url := "http://" + host + "/"

	server := &http.Server{
		Handler:           web.New(book, pings, *data).Handler(host),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	fmt.Printf("Watcher Pinger %s is pinging %d addresses from %s\n", version, len(book.IPs()), *data)
	fmt.Printf("The page is at %s, closing this window stops it.\n", url)
	if !*noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Printf("Cannot open the browser: %v\n", err)
		}
	}

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fail("The page stopped: %v", err)
	}
}

// isPinger tells whether a Watcher Pinger answers at the address.
func isPinger(url string) bool {
	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url + "api/book")
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

func defaultDataFile() string {
	executable, err := os.Executable()
	if err != nil {
		return dataFileName
	}
	return filepath.Join(filepath.Dir(executable), dataFileName)
}

// fail tells what went wrong and waits, so that a window opened by a double click does not close before it is read.
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	fmt.Fprintln(os.Stderr, "Press Enter to close.")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}
