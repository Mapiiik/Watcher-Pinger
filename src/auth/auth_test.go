package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// testKey is a session key of the length needed.
const testKey = "0123456789abcdef0123456789abcdef"

// captureLog collects what the package logs during the test.
func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// page answers with who is asking, as the guard lets it through.
var page = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	user, _ := FromContext(r.Context())
	_, _ = w.Write([]byte("page for " + user.Name))
})

func serve(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNoneLetsEverybodyIn(t *testing.T) {
	g, err := New(context.Background(), Config{Mode: None})
	if err != nil {
		t.Fatal(err)
	}

	w := serve(t, g.Wrap(page), httptest.NewRequest(http.MethodGet, "/api/book", nil))
	if w.Code != http.StatusOK || g.SignOutPath() != "" {
		t.Errorf("status %d, sign out %q", w.Code, g.SignOutPath())
	}
}

// The header is believed only from the proxies, which set it after their own sign-in.
func TestTheHeaderComesFromTheProxyOnly(t *testing.T) {
	g, err := New(context.Background(), Config{Mode: Header, Header: "x-authentik-username", ProxyCIDRs: []string{"192.0.2.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	h := g.Wrap(page)

	for _, tc := range []struct {
		remote, user string
		want         int
	}{
		{"192.0.2.10:4000", "jdoe", http.StatusOK},
		{"192.0.2.10:4000", "", http.StatusForbidden},
		{"198.51.100.7:4000", "jdoe", http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		if tc.user != "" {
			r.Header.Set("X-Authentik-Username", tc.user)
		}
		w := serve(t, h, r)
		if w.Code != tc.want {
			t.Errorf("%s as %q: status %d, want %d", tc.remote, tc.user, w.Code, tc.want)
		}
		if tc.want == http.StatusOK && w.Body.String() != "page for jdoe" {
			t.Errorf("body %q", w.Body.String())
		}
	}
}

// A guard that is not set up fully keeps the server from starting rather than letting everybody in.
func TestAnIncompleteSetUpIsRefused(t *testing.T) {
	for _, cfg := range []Config{
		{Mode: ""},
		{Mode: "basic"},
		{Mode: Header, Header: "X-User"},
		{Mode: Header, Header: "X-User", ProxyCIDRs: []string{"not a network"}},
		{Mode: OIDC, Issuer: "https://login.example.com", ClientID: "pinger"},
		{Mode: OIDC, Issuer: "https://login.example.com", ClientID: "pinger", ClientSecret: "s", RedirectURL: "https://pinger.example.com/"},
	} {
		if _, err := New(context.Background(), cfg); err == nil {
			t.Errorf("%+v was taken", cfg)
		}
	}
}

func TestASignedValueCannotBeChanged(t *testing.T) {
	s := signer{key: []byte("key")}
	now := time.Now()
	signed, err := s.sign("purpose", User{Name: "jdoe"}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	var user User
	expires, err := s.open("purpose", signed, &user, now)
	if err != nil || user.Name != "jdoe" || expires.Unix() != now.Add(time.Minute).Unix() {
		t.Fatalf("%v, %+v, runs out %v", err, user, expires)
	}
	if _, err := s.open("other", signed, &user, now); err == nil {
		t.Error("taken for another purpose")
	}
	if _, err := s.open("purpose", signed, &user, now.Add(2*time.Minute)); err == nil {
		t.Error("taken after running out")
	}
	if _, err := (signer{key: []byte("other key")}).open("purpose", signed, &user, now); err == nil {
		t.Error("taken with another key")
	}
	payload, mac, _ := strings.Cut(signed, ".")
	changed := base64.RawURLEncoding.EncodeToString([]byte(`{"v":{"name":"admin"},"e":9999999999}`))
	if _, err := s.open("purpose", changed+"."+mac, &user, now); err == nil || payload == changed {
		t.Error("a changed value was taken")
	}
}

func TestTheWayBackStaysOnTheServer(t *testing.T) {
	for given, want := range map[string]string{
		"/":                    "/",
		"/?tab=customers":      "/?tab=customers",
		"":                     "/",
		"https://evil.example": "/",
		"//evil.example/":      "/",
		"/\\evil.example":      "/",
		"/auth/login":          "/",
		// a browser drops the tab and goes to //evil.example
		"/\t/evil.example/phish": "/",
		"/\n/evil.example":       "/",
		"/\r/evil.example":       "/",
		"/\x7f/evil.example":     "/",
		"/x\\..\\..\\evil":       "/",
		"/%2F%2Fevil.example":    "/%2F%2Fevil.example",
		"/a/b?c=d#e":             "/a/b?c=d#e",
	} {
		if got := localPath(given); got != want {
			t.Errorf("%q gave %q, want %q", given, got, want)
		}
	}
}

// provider stands in for the identity provider: discovery, keys, the authorization endpoint the user is sent to and
// the token endpoint the server exchanges the code at.
type provider struct {
	*httptest.Server
	key    *rsa.PrivateKey
	groups []string

	mu         sync.Mutex
	challenges map[string]string
	nonces     map[string]string
}

func newProvider(t *testing.T, groups ...string) *provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{key: key, groups: groups, challenges: map[string]string{}, nonces: map[string]string{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/keys",
			"end_session_endpoint":                  p.URL + "/end-session",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// authorize does what the provider does once the user signs in: it hands out a code for the request.
func (p *provider) authorize(t *testing.T, location string) (code, state string) {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, p.URL+"/authorize") {
		t.Fatalf("sent to %q", location)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" {
		t.Fatalf("no PKCE or nonce in %q", location)
	}
	code = "code-" + q.Get("state")
	p.mu.Lock()
	p.challenges[code] = q.Get("code_challenge")
	p.nonces[code] = q.Get("nonce")
	p.mu.Unlock()
	return code, q.Get("state")
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	code := r.PostForm.Get("code")
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))

	p.mu.Lock()
	challenge, nonce := p.challenges[code], p.nonces[code]
	p.mu.Unlock()
	if challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}

	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	idToken, _ := jwt.Signed(signer).Claims(map[string]any{
		"iss":                p.URL,
		"sub":                "42",
		"aud":                "pinger",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"nonce":              nonce,
		"preferred_username": "jdoe",
		"name":               "Jane Doe",
		"groups":             p.groups,
	}).Serialize()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken,
	})
}

func oidcGuard(t *testing.T, p *provider, groups ...string) *Guard {
	t.Helper()
	g, err := New(context.Background(), Config{
		Mode: OIDC, Issuer: p.URL, ClientID: "pinger", ClientSecret: "secret",
		RedirectURL: "https://pinger.example.com/auth/callback", Groups: groups, SessionKey: []byte(testKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// request carries the cookies set so far, as a browser would.
func request(target string, cookies []*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		if c.MaxAge >= 0 && c.Value != "" {
			r.AddCookie(c)
		}
	}
	return r
}

// signIn goes the whole way from the page to the provider and back, and returns the answer of the callback.
func signIn(t *testing.T, p *provider, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	w := serve(t, h, request("/?tab=customers", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/auth/login?return=%2F%3Ftab%3Dcustomers" {
		t.Fatalf("the page: status %d, location %q", w.Code, w.Header().Get("Location"))
	}

	w = serve(t, h, request(w.Header().Get("Location"), nil))
	code, state := p.authorize(t, w.Header().Get("Location"))

	return serve(t, h, request("/auth/callback?code="+code+"&state="+state, w.Result().Cookies()))
}

func TestOIDCSignsInAndKeepsTheSession(t *testing.T) {
	logged := captureLog(t)
	p := newProvider(t)
	g := oidcGuard(t, p)
	h := g.Wrap(page)

	if w := serve(t, h, request("/api/book", nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("the API without a session: status %d", w.Code)
	}

	w := signIn(t, p, h)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/?tab=customers" {
		t.Fatalf("the callback: status %d, location %q, %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	session := w.Result().Cookies()
	for _, c := range session {
		if c.Name == sessionCookie && (!c.Secure || !c.HttpOnly) {
			t.Errorf("the session cookie is not kept to HTTPS and away from scripts: %+v", c)
		}
	}

	w = serve(t, h, request("/api/book", session))
	if w.Code != http.StatusOK || w.Body.String() != "page for jdoe" {
		t.Errorf("with the session: status %d, %q", w.Code, w.Body.String())
	}

	w = serve(t, h, request("/auth/logout", session))
	if w.Header().Get("Location") != p.URL+"/end-session" {
		t.Errorf("signed out to %q", w.Header().Get("Location"))
	}

	for _, line := range []string{"INFO signed in user=jdoe client=192.0.2.1", "INFO signed out user=jdoe client=192.0.2.1"} {
		if !strings.Contains(logged.String(), line) {
			t.Errorf("the log lacks %q in %q", line, logged.String())
		}
	}
}

// A request of a session ends when the session does, the stream of states with it.
func TestARequestEndsWithTheSession(t *testing.T) {
	p := newProvider(t)
	var deadline time.Time
	h := oidcGuard(t, p).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
	}))

	signedIn := time.Now()
	session := signIn(t, p, h).Result().Cookies()
	serve(t, h, request("/api/events", session))

	if want := signedIn.Add(12 * time.Hour); deadline.Before(want.Add(-time.Minute)) || deadline.After(want.Add(time.Minute)) {
		t.Errorf("the request ends at %v, the session at about %v", deadline, want)
	}
}

// A short key would let anybody find it from a login cookie and sign a session of their own.
func TestAShortSessionKeyIsRefused(t *testing.T) {
	p := newProvider(t)
	_, err := New(context.Background(), Config{
		Mode: OIDC, Issuer: p.URL, ClientID: "pinger", ClientSecret: "secret",
		RedirectURL: "https://pinger.example.com/auth/callback", SessionKey: []byte(testKey[1:]),
	})
	if err == nil || !strings.Contains(err.Error(), "PINGER_SESSION_KEY") {
		t.Errorf("err = %v", err)
	}
}

// Coming back with a state other than the one sent away with is refused, and so is a code the provider never gave.
func TestOIDCRefusesASignInItDidNotStart(t *testing.T) {
	p := newProvider(t)
	h := oidcGuard(t, p).Wrap(page)

	w := serve(t, h, request("/auth/login", nil))
	code, _ := p.authorize(t, w.Header().Get("Location"))
	cookies := w.Result().Cookies()

	if w := serve(t, h, request("/auth/callback?code="+code+"&state=forged", cookies)); w.Code != http.StatusBadRequest {
		t.Errorf("another state: status %d", w.Code)
	}
	if w := serve(t, h, request("/auth/callback?code="+code+"&state=x", nil)); w.Code != http.StatusBadRequest {
		t.Errorf("no sign-in started: status %d", w.Code)
	}
}

func TestOIDCLetsInOnlyTheGroupsGiven(t *testing.T) {
	logged := captureLog(t)
	p := newProvider(t, "customers")
	if w := signIn(t, p, oidcGuard(t, p, "staff").Wrap(page)); w.Code != http.StatusForbidden {
		t.Errorf("another group: status %d", w.Code)
	}
	if line := `WARN sign-in refused user=jdoe client=192.0.2.1 reason="in none of the groups allowed"`; !strings.Contains(logged.String(), line) {
		t.Errorf("the log lacks %q in %q", line, logged.String())
	}

	p = newProvider(t, "customers", "staff")
	if w := signIn(t, p, oidcGuard(t, p, "staff").Wrap(page)); w.Code != http.StatusFound {
		t.Errorf("a member: status %d", w.Code)
	}
}
