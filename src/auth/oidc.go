package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	pathPrefix   = "/auth/"
	loginPath    = "/auth/login"
	callbackPath = "/auth/callback"
	logoutPath   = "/auth/logout"

	sessionCookie = "pinger_session"
	loginCookie   = "pinger_login"

	// signInTime is how long the user has for the provider, from leaving the page to coming back
	signInTime = 10 * time.Minute

	// minSessionKey is the shortest key that signs the cookies: anybody gets a login cookie, and with a short key
	// could find the key from it and sign a session of their own
	minSessionKey = 32
)

// oidcFlow signs the user in at the provider with the authorization code flow and PKCE, and keeps who it is in a
// signed cookie. The server keeps no sessions, so it can be started again or run more than once.
type oidcFlow struct {
	oauth      oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string
	groups     []string
	ttl        time.Duration
	secure     bool
	cookies    signer
	now        func() time.Time
	mux        *http.ServeMux
}

// login is what the flow keeps between leaving for the provider and coming back.
type login struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"c"`
	Return   string `json:"r"`
}

func newOIDCFlow(ctx context.Context, cfg Config) (*oidcFlow, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return nil, errors.New("OIDC needs the issuer, the client ID, the client secret and the redirect URL")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Host == "" || redirect.Path != callbackPath {
		return nil, fmt.Errorf("the redirect URL has to end at %s on the server, not %q", callbackPath, cfg.RedirectURL)
	}

	discovery, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discovery, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("the provider %s: %w", cfg.Issuer, err)
	}
	var metadata struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = provider.Claims(&metadata)

	key := cfg.SessionKey
	if len(key) > 0 && len(key) < minSessionKey {
		return nil, fmt.Errorf("PINGER_SESSION_KEY has %d bytes, at least %d are needed: openssl rand -base64 48",
			len(key), minSessionKey)
	}
	if len(key) == 0 {
		key = make([]byte, minSessionKey)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		log.Println("PINGER_SESSION_KEY is not set: a key of its own, a start signs everyone in again")
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}

	f := &oidcFlow{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		endSession: metadata.EndSession,
		groups:     cfg.Groups,
		ttl:        ttl,
		secure:     redirect.Scheme == "https",
		cookies:    signer{key: key},
		now:        time.Now,
		mux:        http.NewServeMux(),
	}
	if len(f.groups) > 0 {
		f.oauth.Scopes = append(f.oauth.Scopes, "groups")
	} else {
		log.Println("OIDC: PINGER_OIDC_GROUPS is empty, whoever the provider lets through sees the network")
	}
	f.mux.HandleFunc("GET "+loginPath, f.login)
	f.mux.HandleFunc("GET "+callbackPath, f.callback)
	f.mux.HandleFunc("GET "+logoutPath, f.logout)
	return f, nil
}

func (f *oidcFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mux.ServeHTTP(w, r)
}

// session is the user of a valid session cookie and when the session runs out.
func (f *oidcFlow) session(r *http.Request) (User, time.Time, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return User{}, time.Time{}, false
	}
	var user User
	expires, err := f.cookies.open(sessionCookie, cookie.Value, &user, f.now())
	if err != nil || user.Name == "" {
		return User{}, time.Time{}, false
	}
	return user, expires, true
}

// login sends the user to the provider, remembering what to check on the way back.
func (f *oidcFlow) login(w http.ResponseWriter, r *http.Request) {
	l := login{
		State:    randomString(),
		Nonce:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		Return:   localPath(r.URL.Query().Get("return")),
	}
	if err := f.setCookie(w, loginCookie, l, f.now().Add(signInTime)); err != nil {
		http.Error(w, "cannot start the sign-in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, f.oauth.AuthCodeURL(l.State, oidc.Nonce(l.Nonce), oauth2.S256ChallengeOption(l.Verifier)),
		http.StatusFound)
}

// callback takes the user back from the provider: the code is exchanged for an ID token, which has to be the one
// asked for, and its user gets a session.
func (f *oidcFlow) callback(w http.ResponseWriter, r *http.Request) {
	var l login
	cookie, err := r.Cookie(loginCookie)
	if err == nil {
		_, err = f.cookies.open(loginCookie, cookie.Value, &l, f.now())
	}
	if err != nil {
		// a sign-in left too long, or a browser that keeps no cookie, which a redirect would send round for ever
		refused(r, "", "no sign-in started in this browser, or it ran out")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `<p>The sign-in ran out or was started in another browser. <a href="%s">Sign in again</a>.</p>`,
			loginPath)
		return
	}
	f.clearCookie(w, loginCookie)

	query := r.URL.Query()
	if problem := query.Get("error"); problem != "" {
		refused(r, "", "the provider refused: "+problem)
		http.Error(w, "the sign-in was refused: "+problem, http.StatusForbidden)
		return
	}
	if query.Get("state") != l.State {
		refused(r, "", "the state does not match the sign-in started")
		http.Error(w, "the sign-in does not match the one started", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	token, err := f.oauth.Exchange(ctx, query.Get("code"), oauth2.VerifierOption(l.Verifier))
	if err != nil {
		refused(r, "", fmt.Sprintf("the code was not exchanged: %v", err))
		http.Error(w, "the provider did not confirm the sign-in", http.StatusBadGateway)
		return
	}
	raw, _ := token.Extra("id_token").(string)
	idToken, err := f.verifier.Verify(ctx, raw)
	if err != nil || idToken.Nonce != l.Nonce {
		refused(r, "", fmt.Sprintf("the ID token is not valid: %v", err))
		http.Error(w, "the provider did not confirm the sign-in", http.StatusBadGateway)
		return
	}

	var claims struct {
		Username string   `json:"preferred_username"`
		Name     string   `json:"name"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		refused(r, "", fmt.Sprintf("the ID token says nobody: %v", err))
		http.Error(w, "the provider did not say who signed in", http.StatusBadGateway)
		return
	}
	name := firstOf(claims.Username, claims.Email, idToken.Subject)
	if len(f.groups) > 0 && !slices.ContainsFunc(claims.Groups, func(g string) bool { return slices.Contains(f.groups, g) }) {
		refused(r, name, "in none of the groups allowed")
		http.Error(w, "your account may not see the pinger", http.StatusForbidden)
		return
	}

	user := User{Name: name, DisplayName: claims.Name}
	if err := f.setCookie(w, sessionCookie, user, f.now().Add(f.ttl)); err != nil {
		http.Error(w, "cannot keep the sign-in", http.StatusInternalServerError)
		return
	}
	slog.Info("signed in", "user", name, "client", clientIP(r))
	http.Redirect(w, r, l.Return, http.StatusFound)
}

// logout ends the session here and at the provider, where it says how.
func (f *oidcFlow) logout(w http.ResponseWriter, r *http.Request) {
	if user, _, ok := f.session(r); ok {
		slog.Info("signed out", "user", user.Name, "client", clientIP(r))
	}
	f.clearCookie(w, sessionCookie)
	target := "/"
	if f.endSession != "" {
		target = f.endSession
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (f *oidcFlow) setCookie(w http.ResponseWriter, name string, value any, expires time.Time) error {
	signed, err := f.cookies.sign(name, value, expires)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    signed,
		Path:     "/",
		Expires:  expires,
		Secure:   f.secure,
		HttpOnly: true,
		// Lax, as the provider sends the user back with a plain GET
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (f *oidcFlow) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   f.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// localPath keeps the way back on this server: a path of its own, nothing that leads elsewhere. A browser drops tabs
// and line breaks from a URL and reads a backslash as a slash, so /<TAB>/elsewhere would lead to another site; no
// control character or backslash is taken at all.
func localPath(path string) string {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, pathPrefix) {
		return "/"
	}
	for _, c := range path {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	if u, err := url.Parse(path); err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return path
}

// refused logs a sign-in turned away, with who it was where the provider said so.
func refused(r *http.Request, user, reason string) {
	slog.Warn("sign-in refused", "user", user, "client", clientIP(r), "reason", reason)
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
