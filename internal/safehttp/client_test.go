package safehttp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oszuidwest/zw-xposter/internal/testutil"
)

func TestClientBlocksRestrictedDestinationBeforeConnecting(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	testutil.NoError(t, err)
	// Trust the local certificate so a missing IP guard cannot hide behind a TLS error.
	transport := client.Transport.(*allowlistTransport).base.(*http.Transport)
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	defer client.CloseIdleConnections()
	resp, err := client.Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "not public") || requests.Load() != 0 {
		t.Fatalf("Get() error=%v, destination requests=%d; want IP rejection before connecting", err, requests.Load())
	}
}

func TestClientClosesIdleConnections(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Remote-Addr", r.RemoteAddr)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := server.Client()
	target, err := url.Parse(server.URL)
	testutil.NoError(t, err)
	client.Transport = &allowlistTransport{
		base:    client.Transport,
		allowed: map[string]struct{}{target.Hostname(): {}},
	}
	defer client.CloseIdleConnections()
	remoteAddress := func() string {
		resp, err := client.Get(server.URL)
		testutil.NoError(t, err)
		testutil.NoError(t, resp.Body.Close())
		return resp.Header.Get("X-Remote-Addr")
	}
	first := remoteAddress()
	testutil.Equal(t, remoteAddress(), first)
	client.CloseIdleConnections()
	if remoteAddress() == first {
		t.Fatal("request reused a connection after CloseIdleConnections")
	}
}

func TestValidateURL(t *testing.T) {
	allowed := map[string]struct{}{
		"www.zuidwestupdate.nl": {},
	}
	tests := []struct {
		name    string
		rawURL  string
		wantErr string
	}{
		{name: "feed host", rawURL: "https://www.zuidwestupdate.nl/article"},
		{name: "case and trailing dot", rawURL: "https://WWW.ZuidWestUpdate.nl./article"},
		{name: "http", rawURL: "http://www.zuidwestupdate.nl/article", wantErr: "https"},
		{name: "other host", rawURL: "https://example.invalid/article", wantErr: "not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := url.Parse(tt.rawURL)
			testutil.NoError(t, err)
			err = validateURL(target, allowed)
			testutil.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestClientValidatesEveryRedirectTarget(t *testing.T) {
	tests := []struct {
		name     string
		location string
		wantErr  string
	}{
		{name: "allowed host", location: "https://www.zuidwestupdate.nl/next"},
		{name: "other host", location: "https://example.invalid/next", wantErr: "not allowed"},
		{name: "downgrade to http", location: "http://www.zuidwestupdate.nl/next", wantErr: "https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New("https://www.zuidwestupdate.nl/feed/", nil)
			testutil.NoError(t, err)
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, tt.location, http.StatusFound)
				}
			}))
			client.Transport.(*allowlistTransport).base = server.Client().Transport

			resp, err := client.Get("https://www.zuidwestupdate.nl/start")
			if err == nil {
				_ = resp.Body.Close()
			}
			testutil.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestBlockRestrictedAddress(t *testing.T) {
	tests := []struct {
		address string
		wantErr bool
	}{
		{address: "93.184.216.34:443"},
		{address: "[2001:4860:4860::8888]:443"},
		{address: "127.0.0.1:443", wantErr: true},
		{address: "10.0.0.1:443", wantErr: true},
		{address: "169.254.169.254:80", wantErr: true},
		{address: "0.0.0.0:443", wantErr: true},
		{address: "224.0.0.1:443", wantErr: true},
		{address: "[::1]:443", wantErr: true},
		{address: "[fc00::1]:443", wantErr: true},
		{address: "[fe80::1%eth0]:443", wantErr: true},
		{address: "[::]:443", wantErr: true},
		{address: "[ff02::1]:443", wantErr: true},
		{address: "hostname.invalid:443", wantErr: true},
		{address: "[::ffff:127.0.0.1]:443", wantErr: true},
		{address: "[::ffff:93.184.216.34]:443"},
		{address: "0.1.2.3:443", wantErr: true},
		{address: "100.64.0.1:443", wantErr: true},
		{address: "100.127.255.254:443", wantErr: true},
		{address: "192.0.0.8:443", wantErr: true},
		{address: "192.0.2.1:443", wantErr: true},
		{address: "192.88.99.1:443", wantErr: true},
		{address: "198.18.0.1:443", wantErr: true},
		{address: "198.19.255.254:443", wantErr: true},
		{address: "198.51.100.1:443", wantErr: true},
		{address: "203.0.113.1:443", wantErr: true},
		{address: "240.0.0.1:443", wantErr: true},
		{address: "255.255.255.255:443", wantErr: true},
		{address: "100.128.0.1:443"},
		{address: "198.20.0.1:443"},
		{address: "[::127.0.0.1]:443", wantErr: true},
		{address: "[64:ff9b::7f00:1]:443", wantErr: true},
		{address: "[64:ff9b:1::1]:443", wantErr: true},
		{address: "[100::1]:443", wantErr: true},
		{address: "[2001::7f00:1]:443", wantErr: true},
		{address: "[2001:2::1]:443", wantErr: true},
		{address: "[2001:db8::1]:443", wantErr: true},
		{address: "[2002:7f00:1::1]:443", wantErr: true},
		{address: "[3fff::1]:443", wantErr: true},
		{address: "[5f00::1]:443", wantErr: true},
		{address: "[2002::1%eth0]:443", wantErr: true},
		{address: "[2a00:1450:4001::1]:443"},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			err := blockRestrictedAddress("tcp", tt.address, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("blockRestrictedAddress() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		feedURL string
		hosts   []string
		wantErr string
	}{
		{name: "insecure feed", feedURL: "http://www.zuidwestupdate.nl/feed/", wantErr: "https"},
		{name: "host with scheme", feedURL: "https://www.zuidwestupdate.nl/feed/", hosts: []string{"https://media.example"}, wantErr: "scheme or path"},
		{name: "host with port", feedURL: "https://www.zuidwestupdate.nl/feed/", hosts: []string{"media.example:8443"}, wantErr: "valid host"},
		{name: "empty host", feedURL: "https://www.zuidwestupdate.nl/feed/", hosts: []string{" \t "}, wantErr: "empty host"},
		{name: "normalized extra host", feedURL: "https://www.zuidwestupdate.nl/feed/", hosts: []string{" \t MEDIA.Example. \n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := New(tt.feedURL, tt.hosts)
			testutil.ErrorContains(t, err, tt.wantErr)
			if err == nil {
				target, err := url.Parse("https://media.example/image.jpg")
				testutil.NoError(t, err)
				testutil.NoError(t, validateURL(target, client.Transport.(*allowlistTransport).allowed))
			}
		})
	}
}
