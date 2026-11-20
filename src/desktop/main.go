// Watcher Pinger pings the network Watcher NMS knows and shows it as a tree in the browser. It runs from a file
// handed out by Watcher NMS, so that it works in a part of the network cut off from the rest. On Windows it keeps an
// icon in the notification area, coloured by how the network is, instead of a console window.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"watcher-pinger/src/addressbook"
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

// pinger pings, through the ICMP helper on Windows and over an ICMP socket elsewhere.
type pinger interface {
	monitor.Pinger
	Close() error
}

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
	inConsole := flag.Bool("console", false, "keep to the console instead of the icon in the notification area")
	showVersion := flag.Bool("version", false, "tell the version and stop")
	flag.Parse()

	setUpConsole(*inConsole)
	if *showVersion {
		tell("Watcher Pinger " + version)
		return
	}

	key, err := pageKey()
	if err != nil {
		fail("Cannot keep the key of the page: %v", err)
	}

	// started a second time, it shows the page of the one running; another user of the computer gets a pinger of
	// their own on the next free port
	listener, running, err := listenFor(*listen, key, *listen == defaultListen)
	if running != "" {
		fmt.Printf("Watcher Pinger is running already, its page is at %s\n", running)
		if !*noBrowser {
			_ = openBrowser(withKey(running, key))
		}
		return
	}
	if err != nil {
		fail("Cannot listen on %s: %v", *listen, err)
	}

	book, err := addressbook.Load(*data)
	if err != nil {
		fail("Cannot read %s: %v", *data, err)
	}

	pinger, err := openPinger()
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

	// Ctrl+C, and on Windows the console window closed, which Go tells as SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go pings.Run(ctx)

	host := listener.Addr().String()
	url := withKey("http://"+host+"/", key)

	page := web.New(book, pings, *data)
	// only the browser this pinger opens gets in, not another user of the same computer
	page.Key = key
	// a new file from Watcher NMS is read on a click, without starting the pinger again
	page.Reload = func(context.Context) (*addressbook.Book, error) { return addressbook.Load(*data) }
	server := &http.Server{
		Handler:           page.Handler([]string{host}, nil),
		ReadHeaderTimeout: 10 * time.Second,
	}
	stopped := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stopped <- err
		}
	}()

	fmt.Printf("Watcher Pinger %s is pinging %d addresses from %s\n", version, len(book.IPs()), *data)
	d := &desktop{url: url, page: page, pings: pings, stopped: stopped, console: *inConsole}
	if !*noBrowser {
		d.openPage()
	}

	err = d.run(ctx)
	stop()
	_ = server.Close()
	if err != nil {
		fail("The page stopped: %v", err)
	}
}

// desktop is the pinger on the computer of the one looking at it: the page in the browser and the icon in the
// notification area, or the console.
type desktop struct {
	url   string
	page  *web.Server
	pings *monitor.Monitor
	// stopped tells why the page stopped being served
	stopped <-chan error
	// console keeps the pinger to the console rather than the icon
	console bool
}

func (d *desktop) openPage() {
	if err := openBrowser(d.url); err != nil {
		tell(fmt.Sprintf("Cannot open the browser, the page is at %s: %v", d.url, err))
	}
}

func defaultDataFile() string {
	executable, err := os.Executable()
	if err != nil {
		return dataFileName
	}
	return filepath.Join(filepath.Dir(executable), dataFileName)
}
