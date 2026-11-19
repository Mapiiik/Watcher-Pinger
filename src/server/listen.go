package main

// How the server listens is that of Watcher Agent (src/main/tls.go, proxy.go and drain.go there): ACME through
// TLS-ALPN on the HTTPS port where it has a name, a self-signed certificate where it has none, and the PROXY header
// from the proxies allowed to send it. It differs in what a page that streams needs: no write timeout, and the streams
// end on shutdown rather than holding it up.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/crypto/acme/autocert"
)

// TLS 1.2 suites with ECDHE and authenticated encryption only; TLS 1.3 suites are all of that kind.
var tls12CipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// shutdownTimeout is how long running requests get to finish, under a stop_grace_period of 60 s.
const shutdownTimeout = 50 * time.Second

// newServer gives a server the timeouts that keep a silent client from holding its connection for ever. There is no
// write timeout, as the states stream to the page for as long as it is open; the page sends a heartbeat that tells a
// dead one apart.
func newServer(addr string, handler http.Handler) *http.Server {
	streams, endStreams := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return streams },
	}
	srv.RegisterOnShutdown(endStreams)
	return srv
}

// tlsConfig uses ACME where the server has a name, otherwise a self-signed certificate made at every start.
func tlsConfig(cfg Config) (*tls.Config, error) {
	if cfg.Hostname == "" {
		log.Println("TLS: a self-signed certificate, as PINGER_HOSTNAME is not set")
		cert, err := selfSignedCertificate()
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, CipherSuites: tls12CipherSuites}, nil
	}

	log.Printf("TLS: ACME for %s", cfg.Hostname)
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Hostname),
		Cache:      autocert.DirCache(cfg.CertDir),
	}
	tlsCfg := m.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12
	tlsCfg.CipherSuites = tls12CipherSuites
	return tlsCfg, nil
}

func selfSignedCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "watcher-pinger", Organization: []string{"Watcher"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// proxyListener uses the PROXY header only from the given networks and lets it be missing there. From anywhere else
// the header is dropped and the connection keeps its real address. Without any network it is used from everybody.
func proxyListener(ln net.Listener, allowCIDRs []string) net.Listener {
	if len(allowCIDRs) == 0 {
		log.Println("WARNING: PINGER_PROXY_ALLOW_CIDRS is not set, anybody who reaches this port can claim any address")
		return &proxyproto.Listener{Listener: ln}
	}

	var networks []*net.IPNet
	for _, c := range allowCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			networks = append(networks, n)
		} else {
			log.Printf("WARNING: PINGER_PROXY_ALLOW_CIDRS: not a network: %q", c)
		}
	}
	return &proxyproto.Listener{
		Listener: ln,
		ConnPolicy: func(opts proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			if addr, ok := opts.Upstream.(*net.TCPAddr); ok {
				for _, n := range networks {
					if n.Contains(addr.IP) {
						return proxyproto.USE, nil
					}
				}
			}
			return proxyproto.IGNORE, nil
		},
	}
}

// httpsRedirect sends plain HTTP requests to the same host and path on the HTTPS port.
func httpsRedirect(listenHTTPS string) http.Handler {
	_, port, _ := net.SplitHostPort(listenHTTPS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.TrimSuffix(strings.TrimPrefix(r.Host, "["), "]")
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		u := *r.URL
		u.Scheme = "https"
		u.Host = host
		if port != "443" {
			u.Host = net.JoinHostPort(host, port)
		}
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
	})
}

// draining is set on shutdown, from then on new requests get 503.
var draining atomic.Bool

func drain(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			http.Error(w, "the server is shutting down", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// shutdown refuses new requests, ends the streams and waits for the rest, for at most the timeout.
func shutdown(servers ...*http.Server) {
	draining.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Shutdown of the server on %s: %v", srv.Addr, err)
		}
	}
}
