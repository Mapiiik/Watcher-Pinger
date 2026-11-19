package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"watcher-pinger/src/auth"
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

	Monitor monitor.Options
}

// LoadConfig reads the configuration and tells what is missing or wrong, all of it at once.
func LoadConfig() (Config, error) {
	var problems []string
	env := environment{problems: &problems}

	cfg := Config{
		ListenHTTP:       env.text("PINGER_LISTEN_HTTP", "0.0.0.0:80"),
		ListenHTTPS:      env.optional("PINGER_LISTEN_HTTPS", "0.0.0.0:443"),
		Hostname:         env.text("PINGER_HOSTNAME", ""),
		CertDir:          env.text("PINGER_CERT_DIR", "/app/certs"),
		UseProxyProtocol: env.boolean("PINGER_USE_PROXY_PROTOCOL"),
		ProxyAllowCIDRs:  env.list("PINGER_PROXY_ALLOW_CIDRS"),
		NMSURL:           env.text("PINGER_NMS_URL", ""),
		NMSToken:         env.text("PINGER_NMS_TOKEN", ""),
		NMSInterval:      env.duration("PINGER_NMS_INTERVAL", time.Hour),
		DataFile:         env.text("PINGER_DATA", "/app/data/watcher-pinger.json"),
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
	if cfg.NMSURL != "" && cfg.NMSToken == "" {
		problems = append(problems, "PINGER_NMS_TOKEN is missing: the key of a user of the role watcher-pinger")
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
