// Package auth lets in only who may see the page, the way the server is set up: through the identity provider of the
// organisation (OIDC), through a header that a proxy in front of the server sets, or without asking at all.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Mode is how the server tells who is asking.
type Mode string

const (
	// None asks nobody: the server is protected some other way, or not at all.
	None Mode = "none"
	// Header takes the user from a header set by a proxy in front, forward auth of Authentik say.
	Header Mode = "header"
	// OIDC signs the user in at the identity provider and keeps a session in a cookie.
	OIDC Mode = "oidc"
)

// Config says how the server tells who is asking.
type Config struct {
	Mode Mode

	// OIDC: the issuer, the client registered there and where the provider sends the user back to
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Groups lets in only members of any of them, empty for anybody the provider lets through
	Groups []string
	// SessionKey signs the cookies, empty for a key of its own every start, which signs everyone in again quietly
	SessionKey []byte
	// SessionTTL is how long a sign-in lasts
	SessionTTL time.Duration

	// Header: the header with the user name and the proxies it is taken from
	Header     string
	ProxyCIDRs []string
}

// User is who is asking.
type User struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
}

// Guard lets a request through to the page or turns it away.
type Guard struct {
	mode    Mode
	oidc    *oidcFlow
	header  string
	proxies []*net.IPNet
}

type userKey struct{}

// New sets the guard up. For OIDC it asks the provider for its configuration, so the provider has to be reachable.
func New(ctx context.Context, cfg Config) (*Guard, error) {
	g := &Guard{mode: cfg.Mode}

	switch cfg.Mode {
	case None:
		log.Println("WARNING: PINGER_AUTH is none, anybody who reaches the server sees the network")
	case Header:
		if cfg.Header == "" || len(cfg.ProxyCIDRs) == 0 {
			return nil, errors.New("the header mode needs the header and the networks of the proxies")
		}
		proxies, err := parseCIDRs(cfg.ProxyCIDRs)
		if err != nil {
			return nil, err
		}
		g.header = http.CanonicalHeaderKey(cfg.Header)
		g.proxies = proxies
	case OIDC:
		flow, err := newOIDCFlow(ctx, cfg)
		if err != nil {
			return nil, err
		}
		g.oidc = flow
	default:
		return nil, fmt.Errorf("unknown mode %q, one of none, header and oidc expected", cfg.Mode)
	}

	return g, nil
}

// Mode is how the guard tells who is asking.
func (g *Guard) Mode() Mode {
	return g.mode
}

// SignOutPath is where the page signs the user out, empty where the server cannot.
func (g *Guard) SignOutPath() string {
	if g.mode == OIDC {
		return logoutPath
	}
	return ""
}

// Wrap answers the sign-in itself and lets through to next only a request it knows the user of. A request of a session
// ends when the session does, so that the stream of states stops with it and the page asks for a sign-in.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.oidc != nil && strings.HasPrefix(r.URL.Path, pathPrefix) {
			g.oidc.ServeHTTP(w, r)
			return
		}

		user, expires, ok := g.user(r)
		if !ok {
			g.refuse(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), userKey{}, user)
		if !expires.IsZero() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, expires)
			defer cancel()
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// FromContext tells who is asking, where the guard knows.
func FromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userKey{}).(User)
	return user, ok && user.Name != ""
}

// user is who asks and when their session runs out, zero where it does not.
func (g *Guard) user(r *http.Request) (User, time.Time, bool) {
	switch g.mode {
	case Header:
		if !g.fromProxy(r) {
			slog.Warn("request refused", "client", clientIP(r), "path", r.URL.Path, "reason", "not from the proxy")
			return User{}, time.Time{}, false
		}
		name := strings.TrimSpace(r.Header.Get(g.header))
		if name == "" {
			slog.Warn("request refused", "client", clientIP(r), "path", r.URL.Path, "reason", "no user in "+g.header)
		}
		return User{Name: name}, time.Time{}, name != ""
	case OIDC:
		return g.oidc.session(r)
	default:
		return User{}, time.Time{}, true
	}
}

// refuse sends a page to the sign-in and tells a request of the API that it is not signed in.
func (g *Guard) refuse(w http.ResponseWriter, r *http.Request) {
	if g.mode != OIDC {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not signed in", "signIn": loginPath})
		return
	}
	http.Redirect(w, r, loginPath+"?return="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
}

// clientIP is the address of who asks, the one from the PROXY header behind a proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (g *Guard) fromProxy(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	for _, n := range g.proxies {
		if ip != nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDRs(cidrs []string) ([]*net.IPNet, error) {
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("not a network: %q", c)
		}
		networks = append(networks, n)
	}
	return networks, nil
}
