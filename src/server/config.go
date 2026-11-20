package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"watcher-pinger/src/auth"
	"watcher-pinger/src/infra/icmpsocket"
	"watcher-pinger/src/monitor"
)

// Config is the server's configuration from the PINGER_* environment variables.
type Config struct {
	// ListenHTTP redirects to HTTPS, or serves the page itself where ListenHTTPS is empty, for TLS ended in front
	ListenHTTP  string
	ListenHTTPS string

	// Hostname is the name of the server: its ACME certificate and the only name it answers, empty for a
	// self-signed certificate and any name
	Hostname string
	CertDir  string

	UseProxyProtocol bool
	// who may send a PROXY header, empty for anybody
	ProxyAllowCIDRs []string

	Auth auth.Config

	// NMSURL is the Watcher NMS the network is fetched from at every NMSInterval, with the key of a user of the role
	// watcher-pinger; empty for the data file alone
	NMSURL      string
	NMSToken    string
	NMSInterval time.Duration
	// DataFile is the file handed out by Watcher NMS: the network itself, or the copy of the last one fetched
	DataFile string

	// ICMP is the kind of socket pinged over: auto, unprivileged or raw
	ICMP icmpsocket.Kind

	Monitor monitor.Options
}

// LoadConfig reads the configuration and tells what is missing or wrong, all of it at once.
func LoadConfig() (Config, error) {
	var problems []string
	env := environment{problems: &problems}

	cfg := Config{
		ListenHTTP:  env.text("PINGER_LISTEN_HTTP", "0.0.0.0:80"),
		ListenHTTPS: env.optional("PINGER_LISTEN_HTTPS", "0.0.0.0:443"),
		// a browser sends the host in lower case, a name in capitals would turn every request away
		Hostname:         strings.ToLower(env.text("PINGER_HOSTNAME", "")),
		CertDir:          env.text("PINGER_CERT_DIR", "/app/certs"),
		UseProxyProtocol: env.boolean("PINGER_USE_PROXY_PROTOCOL"),
		ProxyAllowCIDRs:  env.list("PINGER_PROXY_ALLOW_CIDRS"),
		NMSURL:           env.text("PINGER_NMS_URL", ""),
		NMSToken:         env.text("PINGER_NMS_TOKEN", ""),
		NMSInterval:      env.duration("PINGER_NMS_INTERVAL", time.Hour),
		DataFile:         env.text("PINGER_DATA", "/app/data/watcher-pinger.json"),
		ICMP:             icmpsocket.Kind(env.text("PINGER_ICMP", string(icmpsocket.Auto))),
		Monitor: monitor.Options{
			Timeout:         env.duration("PINGER_TIMEOUT", monitor.DefaultOptions.Timeout),
			WatchedInterval: env.duration("PINGER_WATCHED_INTERVAL", monitor.DefaultOptions.WatchedInterval),
			DeviceInterval:  env.duration("PINGER_DEVICE_INTERVAL", monitor.DefaultOptions.DeviceInterval),
			IdleInterval:    env.duration("PINGER_IDLE_INTERVAL", monitor.DefaultOptions.IdleInterval),
			Workers:         env.integer("PINGER_WORKERS", monitor.DefaultOptions.Workers),
		},
	}

	cfg.Auth = auth.Config{
		Mode:         auth.Mode(env.text("PINGER_AUTH", "")),
		Issuer:       env.text("PINGER_OIDC_ISSUER", ""),
		ClientID:     env.text("PINGER_OIDC_CLIENT_ID", ""),
		ClientSecret: env.text("PINGER_OIDC_CLIENT_SECRET", ""),
		RedirectURL:  env.text("PINGER_OIDC_REDIRECT_URL", ""),
		Groups:       env.list("PINGER_OIDC_GROUPS"),
		SessionKey:   []byte(env.text("PINGER_SESSION_KEY", "")),
		SessionTTL:   env.duration("PINGER_SESSION_TTL", 12*time.Hour),
		Header:       env.text("PINGER_AUTH_HEADER", "X-Authentik-Username"),
		ProxyCIDRs:   env.list("PINGER_AUTH_PROXY_CIDRS"),
	}
	if cfg.Auth.Mode == "" {
		problems = append(problems, "PINGER_AUTH is missing: oidc, header or none")
	}
	if cfg.Auth.Mode == auth.OIDC && cfg.Auth.RedirectURL == "" {
		if cfg.Hostname == "" {
			problems = append(problems, "PINGER_OIDC_REDIRECT_URL is missing, and there is no PINGER_HOSTNAME to make it of")
		}
		cfg.Auth.RedirectURL = "https://" + cfg.Hostname + "/auth/callback"
	}
	switch cfg.ICMP {
	case icmpsocket.Auto, icmpsocket.Unprivileged, icmpsocket.Raw:
	default:
		problems = append(problems, fmt.Sprintf("PINGER_ICMP is not auto, unprivileged or raw: %q", cfg.ICMP))
	}
	if cfg.NMSURL != "" && cfg.NMSToken == "" {
		problems = append(problems, "PINGER_NMS_TOKEN is missing: the key of a user of the role watcher-pinger")
	}
	// the key travels with every request, so only encrypted
	if u, err := url.Parse(cfg.NMSURL); cfg.NMSURL != "" && (err != nil || u.Scheme != "https" || u.Host == "") {
		problems = append(problems, fmt.Sprintf("PINGER_NMS_URL is not an https address: %q", cfg.NMSURL))
	}

	// Behind a PROXY header the address of a request is the one the header claims, so who may send it has to be
	// known. The header mode checks the address of the proxy setting the user, which the PROXY header replaces.
	if cfg.UseProxyProtocol {
		if len(cfg.ProxyAllowCIDRs) == 0 {
			problems = append(problems, "PINGER_PROXY_ALLOW_CIDRS is missing: the networks of the proxy sending "+
				"the PROXY header, anybody else could claim any address")
		}
		if cfg.Auth.Mode == auth.Header {
			problems = append(problems, "PINGER_AUTH=header does not go with PINGER_USE_PROXY_PROTOCOL: the address "+
				"of the proxy setting the user is lost behind the PROXY header")
		}
	}
	for _, c := range cfg.ProxyAllowCIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			problems = append(problems, fmt.Sprintf("PINGER_PROXY_ALLOW_CIDRS: not a network: %q", c))
		}
	}

	if len(problems) > 0 {
		return cfg, errors.New(strings.Join(problems, "; "))
	}
	return cfg, nil
}

// environment reads the variables and collects what is wrong with them.
type environment struct {
	problems *[]string
}

func (e environment) text(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// optional is like text, but a variable set empty means nothing rather than the default.
func (e environment) optional(key, def string) string {
	v, set := os.LookupEnv(key)
	if !set {
		return def
	}
	return strings.TrimSpace(v)
}

func (e environment) list(key string) []string {
	return strings.Fields(os.Getenv(key))
}

func (e environment) boolean(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*e.problems = append(*e.problems, fmt.Sprintf("%s is not true or false: %q", key, v))
	}
	return b
}

func (e environment) integer(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil || i <= 0 {
		*e.problems = append(*e.problems, fmt.Sprintf("%s is not a positive number: %q", key, v))
		return def
	}
	return i
}

func (e environment) duration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		*e.problems = append(*e.problems, fmt.Sprintf("%s is not a duration such as 30s or 1h: %q", key, v))
		return def
	}
	return d
}
