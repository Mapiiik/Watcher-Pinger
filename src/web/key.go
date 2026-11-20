package web

import (
	"crypto/subtle"
	"encoding/json"
	"html"
	"net"
	"net/http"
	"strings"
)

// KeyCookie and the port are the name of the cookie a browser keeps the key in, as a cookie of 127.0.0.1 is shared by
// every port.
const KeyCookie = "pinger_key_"

// withoutKey are the files the page without a key shows, which say nothing about the network.
var withoutKey = map[string]bool{"/favicon.svg": true, "/favicon.ico": true, "/apple-touch-icon.png": true}

// keyed lets through only the browser of whoever started the pinger. The address the pinger opens the page at
// carries the key, which the page keeps in a cookie and drops from the address. Anybody else, another user of the
// same computer say, gets a page saying where the pinger is to be opened from.
func (s *Server) keyed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if withoutKey[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		cookie := KeyCookie + portOf(r.Host)

		if given := r.URL.Query().Get("key"); given != "" {
			if !s.isKey(given) {
				s.refuseWithoutKey(w, r)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cookie,
				Value:    s.Key,
				Path:     "/",
				MaxAge:   400 * 24 * 60 * 60,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			clean := *r.URL
			query := clean.Query()
			query.Del("key")
			clean.RawQuery = query.Encode()
			http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
			return
		}

		if c, err := r.Cookie(cookie); err == nil && s.isKey(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		s.refuseWithoutKey(w, r)
	})
}

func (s *Server) isKey(given string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.Key)) == 1
}

// refuseWithoutKey tells a request of the API that it has no key, and a page where it is to be opened from, in Czech
// for a browser that asks for it.
func (s *Server) refuseWithoutKey(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no key, open the page from Watcher Pinger"})
		return
	}

	text := noKeyEnglish
	lang := "en"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "cs") {
		text, lang = noKeyCzech, "cs"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="` + lang + `">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Watcher Pinger</title>
<link rel="icon" href="favicon.svg" type="image/svg+xml">
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 34rem; margin: 4rem auto; padding: 0 1rem; color: #1f2937; }
h1 { display: flex; align-items: center; gap: .6rem; font-size: 1.3rem; }
h1 img { width: 2rem; height: 2rem; }
li { margin: .3rem 0; }
.note { color: #6b7280; font-size: .9rem; }
@media (prefers-color-scheme: dark) { body { background: #111827; color: #e5e7eb; } .note { color: #9ca3af; } }
</style>
</head>
<body>
<h1><img src="favicon.svg" alt="">` + html.EscapeString(text.title) + `</h1>
<p>` + html.EscapeString(text.lead) + `</p>
<ul>
<li>` + html.EscapeString(text.tray) + `</li>
<li>` + html.EscapeString(text.start) + `</li>
</ul>
<p class="note">` + html.EscapeString(text.note) + `</p>
</body>
</html>
`))
}

type noKeyText struct {
	title, lead, tray, start, note string
}

var noKeyEnglish = noKeyText{
	title: "Open the page from Watcher Pinger",
	lead:  "This page opens only from the program that pings, so that nobody else on this computer sees the network.",
	tray:  "Click the icon of Watcher Pinger in the notification area, or choose Open the page in its menu.",
	start: "Or start Watcher Pinger again; it opens the page by itself.",
	note:  "Where more people are signed in to this computer, each runs a Watcher Pinger of their own.",
}

var noKeyCzech = noKeyText{
	title: "Otevřete stránku z Watcher Pingeru",
	lead:  "Stránka se otevírá jen z programu, který pinguje, aby síť neviděl nikdo další přihlášený k tomuto počítači.",
	tray:  "Klikněte na ikonu Watcher Pingeru v oznamovací oblasti, nebo v její nabídce zvolte Otevřít stránku.",
	start: "Nebo Watcher Pinger spusťte znovu, stránku otevře sám.",
	note:  "Kde je k počítači přihlášeno víc lidí, má každý svůj Watcher Pinger.",
}

// portOf is the port of a host as a request names it, empty without one.
func portOf(host string) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	return ""
}
