package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"watcher-pinger/src/auth"
)

// Everything missing or wrong is told at once, so that a server is set up in one go.
func TestTheConfigurationTellsAllThatIsWrong(t *testing.T) {
	t.Setenv("PINGER_AUTH", "")
	t.Setenv("PINGER_NMS_URL", "https://nms.example.com")
	t.Setenv("PINGER_NMS_TOKEN", "")
	t.Setenv("PINGER_TIMEOUT", "a second")
	t.Setenv("PINGER_USE_PROXY_PROTOCOL", "yes please")

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("taken")
	}
	for _, want := range []string{"PINGER_AUTH", "PINGER_NMS_TOKEN", "PINGER_TIMEOUT", "PINGER_USE_PROXY_PROTOCOL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s not told in %q", want, err)
		}
	}
}

func TestTheConfigurationOfOurServer(t *testing.T) {
	t.Setenv("PINGER_AUTH", "oidc")
	t.Setenv("PINGER_HOSTNAME", "pinger.example.com")
	t.Setenv("PINGER_DATA", "/data/watcher-pinger.json")
	t.Setenv("PINGER_USE_PROXY_PROTOCOL", "true")
	t.Setenv("PINGER_PROXY_ALLOW_CIDRS", "192.168.1.0/24 fd0e::/64")
	t.Setenv("PINGER_DEVICE_INTERVAL", "45s")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Mode != auth.OIDC || cfg.Auth.RedirectURL != "https://pinger.example.com/auth/callback" {
		t.Errorf("sign-in %+v", cfg.Auth)
	}
	if cfg.ListenHTTPS != "0.0.0.0:443" || !cfg.UseProxyProtocol || len(cfg.ProxyAllowCIDRs) != 2 {
		t.Errorf("listening %+v", cfg)
	}
	if cfg.Monitor.DeviceInterval != 45*time.Second || cfg.Monitor.IdleInterval != time.Minute {
		t.Errorf("intervals %+v", cfg.Monitor)
	}
	if cfg.NMSInterval != time.Hour || cfg.DataFile != "/data/watcher-pinger.json" {
		t.Errorf("data from %q every %v into %q", cfg.NMSURL, cfg.NMSInterval, cfg.DataFile)
	}
	if cfg.Auth.Header != "X-Authentik-Username" {
		t.Errorf("header %q", cfg.Auth.Header)
	}
}

// An empty PINGER_LISTEN_HTTPS serves the page on plain HTTP, for TLS ended in front of the server.
func TestHTTPSCanBeLeftToTheProxy(t *testing.T) {
	t.Setenv("PINGER_AUTH", "none")
	t.Setenv("PINGER_DATA", "data.json")
	t.Setenv("PINGER_LISTEN_HTTPS", "")
	t.Setenv("PINGER_LISTEN_HTTP", "0.0.0.0:8080")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenHTTPS != "" || cfg.ListenHTTP != "0.0.0.0:8080" {
		t.Errorf("listening %q, %q", cfg.ListenHTTP, cfg.ListenHTTPS)
	}
	if got := hosts(cfg); got != nil {
		t.Errorf("hosts %v without a name", got)
	}
}

func TestThePageAnswersItsOwnNamesOnly(t *testing.T) {
	cfg := Config{Hostname: "pinger.example.com", ListenHTTPS: "0.0.0.0:8443"}
	if got, want := hosts(cfg), []string{"pinger.example.com", "pinger.example.com:8443"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hosts %v, want %v", got, want)
	}
}

func TestPlainHTTPGoesToHTTPS(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:443":  "https://pinger.example.com/?tab=customers",
		"0.0.0.0:8443": "https://pinger.example.com:8443/?tab=customers",
	} {
		w := httptest.NewRecorder()
		httpsRedirect(listen).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://pinger.example.com/?tab=customers", nil))
		if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != want {
			t.Errorf("%s: %d to %q, want %q", listen, w.Code, w.Header().Get("Location"), want)
		}
	}
}

// What would let anybody claim an address, or send the key of Watcher NMS unencrypted, stops the start.
func TestTheConfigurationRefusesWhatWouldLetAnybodyIn(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"PROXY from anybody": {map[string]string{"PINGER_USE_PROXY_PROTOCOL": "true"}, "PINGER_PROXY_ALLOW_CIDRS is missing"},
		"PROXY in front of the header": {map[string]string{
			"PINGER_AUTH": "header", "PINGER_AUTH_PROXY_CIDRS": "10.0.0.0/8",
			"PINGER_USE_PROXY_PROTOCOL": "true", "PINGER_PROXY_ALLOW_CIDRS": "192.168.1.0/24",
		}, "PINGER_AUTH=header"},
		"a network that is none": {map[string]string{
			"PINGER_USE_PROXY_PROTOCOL": "true", "PINGER_PROXY_ALLOW_CIDRS": "192.168.1.0/24 192.168.1.300/24",
		}, `not a network: "192.168.1.300/24"`},
		"NMS over HTTP": {map[string]string{"PINGER_NMS_URL": "http://nms.example.com", "PINGER_NMS_TOKEN": "key"}, "PINGER_NMS_URL"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PINGER_AUTH", "none")
			t.Setenv("PINGER_DATA", "data.json")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A browser sends the host in lower case, so the name is kept so.
func TestTheHostnameIsInLowerCase(t *testing.T) {
	t.Setenv("PINGER_AUTH", "none")
	t.Setenv("PINGER_DATA", "data.json")
	t.Setenv("PINGER_HOSTNAME", "Pinger.Example.COM")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hostname != "pinger.example.com" {
		t.Errorf("hostname %q", cfg.Hostname)
	}
}
