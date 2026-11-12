package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/go-bumbu/http/middleware"
)

// proxyNet is the trusted proxy range used throughout these tests.
var proxyNet = netip.MustParsePrefix("10.0.0.0/8")

func ipRequest(remoteAddr string, headers map[string][]string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remoteAddr
	for k, vs := range headers {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	return r
}

func TestClientIPDirect(t *testing.T) {
	tcs := []struct {
		name       string
		remoteAddr string
		headers    map[string][]string
		want       string
	}{
		{name: "strips the port", remoteAddr: "203.0.113.7:4321", want: "203.0.113.7"},
		{name: "ipv6 with port", remoteAddr: "[2001:db8::1]:4321", want: "2001:db8::1"},
		{name: "unmaps ipv4-in-ipv6", remoteAddr: "[::ffff:203.0.113.7]:4321", want: "203.0.113.7"},
		{name: "no port", remoteAddr: "203.0.113.7", want: "203.0.113.7"},
		{name: "unparseable is returned as-is", remoteAddr: "@", want: "@"},
		{
			name:       "ignores forwarding headers",
			remoteAddr: "203.0.113.7:4321",
			headers: map[string][]string{
				"X-Real-Ip":       {"198.51.100.1"},
				"X-Forwarded-For": {"198.51.100.2"},
			},
			want: "203.0.113.7",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			if got := middleware.ClientIPDirect(ipRequest(tc.remoteAddr, tc.headers)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPFromRealIP(t *testing.T) {
	extract := middleware.ClientIPFromRealIP(proxyNet)
	tcs := []struct {
		name       string
		remoteAddr string
		realIP     string
		want       string
	}{
		{name: "trusted peer: header is used", remoteAddr: "10.1.2.3:80", realIP: "203.0.113.7", want: "203.0.113.7"},
		{name: "untrusted peer: header is ignored", remoteAddr: "198.51.100.9:80", realIP: "203.0.113.7", want: "198.51.100.9"},
		{name: "trusted peer, header missing", remoteAddr: "10.1.2.3:80", want: "10.1.2.3"},
		{name: "trusted peer, header garbage", remoteAddr: "10.1.2.3:80", realIP: "not-an-ip", want: "10.1.2.3"},
		{name: "header with port", remoteAddr: "10.1.2.3:80", realIP: "203.0.113.7:5555", want: "203.0.113.7"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			h := map[string][]string{}
			if tc.realIP != "" {
				h["X-Real-Ip"] = []string{tc.realIP}
			}
			if got := extract(ipRequest(tc.remoteAddr, h)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPFromXFF(t *testing.T) {
	tcs := []struct {
		name       string
		trusted    []netip.Prefix
		remoteAddr string
		xff        []string
		want       string
	}{
		{
			name:       "untrusted peer: header is ignored",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "198.51.100.9:80",
			xff:        []string{"203.0.113.7"},
			want:       "198.51.100.9",
		},
		{
			name:       "no trusted proxies: header is ignored",
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"203.0.113.7"},
			want:       "10.1.2.3",
		},
		{
			name:       "single proxy",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"203.0.113.7"},
			want:       "203.0.113.7",
		},
		{
			name:       "client-forged leftmost entry is skipped",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"1.1.1.1, 203.0.113.7"},
			want:       "203.0.113.7",
		},
		{
			name:       "trusted hops are skipped right to left",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"1.1.1.1, 203.0.113.7, 10.9.9.9, 10.8.8.8"},
			want:       "203.0.113.7",
		},
		{
			name:       "multiple header lines are one list",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"1.1.1.1, 203.0.113.7", "10.9.9.9"},
			want:       "203.0.113.7",
		},
		{
			name:       "all hops trusted: leftmost is the client",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"10.5.5.5, 10.9.9.9"},
			want:       "10.5.5.5",
		},
		{
			name:       "garbage entry stops the walk at the last trusted hop",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			xff:        []string{"203.0.113.7, bogus, 10.9.9.9"},
			want:       "10.9.9.9",
		},
		{
			name:       "header missing",
			trusted:    []netip.Prefix{proxyNet},
			remoteAddr: "10.1.2.3:80",
			want:       "10.1.2.3",
		},
		{
			name:       "entries with ports and ipv6",
			trusted:    []netip.Prefix{proxyNet, netip.MustParsePrefix("fd00::/8")},
			remoteAddr: "[fd00::1]:80",
			xff:        []string{"[2001:db8::7]:5555, 10.9.9.9:80"},
			want:       "2001:db8::7",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			extract := middleware.ClientIPFromXFF(tc.trusted...)
			r := ipRequest(tc.remoteAddr, map[string][]string{"X-Forwarded-For": tc.xff})
			if got := extract(r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPFromXFFHops(t *testing.T) {
	tcs := []struct {
		name string
		hops int
		xff  []string
		want string
	}{
		{name: "one proxy: rightmost entry", hops: 1, xff: []string{"1.1.1.1, 203.0.113.7"}, want: "203.0.113.7"},
		{name: "two proxies", hops: 2, xff: []string{"1.1.1.1, 203.0.113.7, 10.9.9.9"}, want: "203.0.113.7"},
		{name: "fewer entries than hops: leftmost", hops: 3, xff: []string{"203.0.113.7, 10.9.9.9"}, want: "203.0.113.7"},
		{name: "header missing: peer", hops: 1, want: "10.1.2.3"},
		{name: "zero hops: peer", hops: 0, xff: []string{"203.0.113.7"}, want: "10.1.2.3"},
		{name: "garbage entry: peer", hops: 1, xff: []string{"bogus"}, want: "10.1.2.3"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			extract := middleware.ClientIPFromXFFHops(tc.hops)
			r := ipRequest("10.1.2.3:80", map[string][]string{"X-Forwarded-For": tc.xff})
			if got := extract(r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLog_ClientIP verifies the "ip" log field: by default it is the direct peer (forwarding
// headers are not trusted), and Cfg.ClientIP replaces the extractor.
func TestLog_ClientIP(t *testing.T) {
	tcs := []struct {
		name     string
		clientIP func(*http.Request) string
		want     string
	}{
		{name: "default ignores spoofable headers", want: "ip=10.1.2.3 "},
		{name: "configured extractor", clientIP: middleware.ClientIPFromXFF(proxyNet), want: "ip=203.0.113.7 "},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			m := middleware.New(middleware.Cfg{Logger: logger, ClientIP: tc.clientIP})
			r := ipRequest("10.1.2.3:80", map[string][]string{
				"X-Forwarded-For": {"203.0.113.7"},
				"X-Real-Ip":       {"198.51.100.1"},
			})
			m.Wrap(testHandler(200, "ok")).ServeHTTP(httptest.NewRecorder(), r)

			if got := buf.String(); !strings.Contains(got, tc.want) {
				t.Errorf("expected log to contain %q, got %q", tc.want, got)
			}
		})
	}
}
