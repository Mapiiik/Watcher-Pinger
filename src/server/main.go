// Watcher Pinger server pings the network Watcher NMS knows from a server and shows it as a tree in the browser, to
// whoever signs in. It is the pinger of the technicians made to run all the time: configured from the environment,
// with TLS, the PROXY protocol and a sign-in of its own, so that it needs nothing in front of it, and may have.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"watcher-pinger/src/auth"
	"watcher-pinger/src/infra/icmpsocket"
	"watcher-pinger/src/monitor"
	"watcher-pinger/src/web"
)

// version is set by the build, from the tag of the commit.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "tell the version and stop")
	flag.Parse()
	if *showVersion {
		fmt.Println("Watcher Pinger server", version)
		return
	}

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("Configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
	log.Println("Watcher Pinger stopped")
}

func run(ctx context.Context, cfg Config) error {
	guard, err := auth.New(ctx, cfg.Auth)
	if err != nil {
		return fmt.Errorf("sign-in: %w", err)
	}

	data := newSource(cfg)
	book, err := data.first(ctx)
	if err != nil {
		return fmt.Errorf("no network to ping: %w", err)
	}

	// a family that cannot be pinged is logged and tried again a minute later, neither of them stops the start
	pinger, err := icmpsocket.OpenPingers(cfg.ICMP, func(fam icmpsocket.Family, _ *icmpsocket.Pinger, err error) {
		if err != nil {
			log.Printf("ICMP %s: %v", fam.Name, err)
		}
	})
	if err != nil {
		return fmt.Errorf("cannot ping: %w", err)
	}
	defer func() { _ = pinger.Close() }()

	pings := monitor.New(pinger, book.IPs(), book.DeviceIPs(), cfg.Monitor)
	go pings.Run(ctx)

	page := web.New(book, pings, data.label())
	page.Hosted = true
	page.Reload = data.load
	go data.refresh(ctx, page)
	page.Identity = func(r *http.Request) (string, string) {
		user, ok := auth.FromContext(r.Context())
		if !ok {
			return "", ""
		}
		if user.DisplayName != "" {
			return user.DisplayName, guard.SignOutPath()
		}
		return user.Name, guard.SignOutPath()
	}
	handler := drain(page.Handler(hosts(cfg), guard.Wrap))

	var servers []*http.Server
	var pageServer *http.Server
	if cfg.ListenHTTPS == "" {
		pageServer = newServer(cfg.ListenHTTP, handler)
		servers = append(servers, pageServer)
	} else {
		tlsCfg, err := tlsConfig(cfg)
		if err != nil {
			return err
		}
		pageServer = newServer(cfg.ListenHTTPS, handler)
		pageServer.TLSConfig = tlsCfg
		servers = append(servers, pageServer, newServer(cfg.ListenHTTP, httpsRedirect(cfg.ListenHTTPS)))
	}

	stopped := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { stopped <- serve(cfg, srv, srv == pageServer) }()
	}

	log.Printf("Watcher Pinger %s is pinging %d addresses from %s over ICMP sockets %s, sign-in %s", version,
		len(book.IPs()), data.label(), pinger.Describe(), guard.Mode())

	select {
	case <-ctx.Done():
		log.Println("Shutdown signal received")
		shutdown(servers...)
		return nil
	case err := <-stopped:
		shutdown(servers...)
		return err
	}
}

// serve serves until the server is shut down, which is no error, or fails. The page takes the PROXY header where it is
// set up; the redirect does not need it.
func serve(cfg Config, srv *http.Server, page bool) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	if page && cfg.UseProxyProtocol {
		ln = proxyListener(ln, cfg.ProxyAllowCIDRs)
	}

	log.Printf("Listening on %s (TLS %v, PROXY protocol %v)", srv.Addr, srv.TLSConfig != nil, page && cfg.UseProxyProtocol)
	if srv.TLSConfig != nil {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// hosts are the names the page answers to: the server's own, with the port where it is not the usual one, or any
// where it has none.
func hosts(cfg Config) []string {
	if cfg.Hostname == "" {
		return nil
	}
	names := []string{cfg.Hostname}
	listen := cfg.ListenHTTPS
	if listen == "" {
		listen = cfg.ListenHTTP
	}
	if _, port, err := net.SplitHostPort(listen); err == nil {
		names = append(names, net.JoinHostPort(cfg.Hostname, port))
	}
	return names
}
