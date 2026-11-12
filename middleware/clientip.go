package middleware

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Client IP extractors for Cfg.ClientIP. Each one has the signature
// func(*http.Request) string and returns the address logged as the "ip" field.
//
// Forwarding headers (X-Real-Ip, X-Forwarded-For) are set by whoever sent the request, so they
// are only believed when the request arrived from a proxy you trust; otherwise any client could
// write a fake address into your logs. Pick the extractor that matches your deployment:
//
//   - exposed directly to clients: ClientIPDirect (the default)
//   - behind a proxy that sets X-Real-Ip (e.g. nginx): ClientIPFromRealIP
//   - behind one or more proxies that append to X-Forwarded-For: ClientIPFromXFF
//   - behind proxies with no stable address range (some cloud load balancers): ClientIPFromXFFHops
//
// Every extractor accepts addresses with or without a port, unmaps IPv4-in-IPv6 addresses, and
// falls back to the direct peer when a header is missing or not a valid IP, so a malformed
// header never reaches the log.

// ClientIPDirect returns the address of the direct peer (r.RemoteAddr without its port) and
// ignores all forwarding headers. It cannot be spoofed by the client, and it is the default
// when Cfg.ClientIP is nil. Behind a reverse proxy it returns the proxy's address. A RemoteAddr
// that is not an IP (e.g. a unix socket) is returned unchanged.
func ClientIPDirect(r *http.Request) string {
	if a, ok := parseIP(r.RemoteAddr); ok {
		return a.String()
	}
	return r.RemoteAddr
}

// ClientIPFromRealIP returns the X-Real-Ip header when the direct peer is inside one of the
// trusted proxy ranges, and the direct peer otherwise. A single proxy address is a /32 (IPv4)
// or /128 (IPv6) prefix.
func ClientIPFromRealIP(trusted ...netip.Prefix) func(*http.Request) string {
	proxies := slices.Clone(trusted)
	return func(r *http.Request) string {
		peer, ok := parseIP(r.RemoteAddr)
		if !ok || !inPrefixes(proxies, peer) {
			return ClientIPDirect(r)
		}
		if a, ok := parseIP(r.Header.Get("X-Real-Ip")); ok {
			return a.String()
		}
		return peer.String()
	}
}

// ClientIPFromXFF resolves the client from X-Forwarded-For when the direct peer is inside one of
// the trusted proxy ranges, and returns the direct peer otherwise.
//
// Each proxy appends the address it received the request from, so the left end of the list is
// whatever the client sent and cannot be trusted. The list (all header lines, in order) is walked
// from the right, skipping trusted proxies; the first untrusted address is the client. If every
// entry is trusted the leftmost one is returned, and an invalid entry stops the walk at the last
// trusted hop before it. With no trusted ranges it behaves like ClientIPDirect.
func ClientIPFromXFF(trusted ...netip.Prefix) func(*http.Request) string {
	proxies := slices.Clone(trusted)
	return func(r *http.Request) string {
		peer, ok := parseIP(r.RemoteAddr)
		if !ok || !inPrefixes(proxies, peer) {
			return ClientIPDirect(r)
		}
		client := peer
		entries := xffEntries(r)
		for i := len(entries) - 1; i >= 0; i-- {
			a, ok := parseIP(entries[i])
			if !ok {
				break
			}
			client = a
			if !inPrefixes(proxies, a) {
				break
			}
		}
		return client.String()
	}
}

// ClientIPFromXFFHops resolves the client from X-Forwarded-For by position, for deployments
// whose proxies have no stable address range to list: hops is the number of proxies in front of
// the server, so with one proxy the rightmost entry is the client. When the list is shorter than
// hops the leftmost entry is returned; when the header is missing, the selected entry is invalid,
// or hops < 1, the direct peer is returned.
//
// It does not check where the request came from, so it is only safe when clients cannot reach
// the server without going through those proxies. Prefer ClientIPFromXFF when you can list the
// proxy ranges.
func ClientIPFromXFFHops(hops int) func(*http.Request) string {
	return func(r *http.Request) string {
		entries := xffEntries(r)
		if hops < 1 || len(entries) == 0 {
			return ClientIPDirect(r)
		}
		idx := max(len(entries)-hops, 0)
		if a, ok := parseIP(entries[idx]); ok {
			return a.String()
		}
		return ClientIPDirect(r)
	}
}

// xffEntries returns the X-Forwarded-For list across all header lines, in order.
func xffEntries(r *http.Request) []string {
	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(line, ",")...)
	}
	return entries
}

// parseIP parses an address with or without a port ("1.2.3.4", "1.2.3.4:80", "[::1]:80"),
// unmapping IPv4-in-IPv6 and dropping any IPv6 zone so prefix matching works.
func parseIP(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

func inPrefixes(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
