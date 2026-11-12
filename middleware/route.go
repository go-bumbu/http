package middleware

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
)

type routeKey struct{}

// routeSlot travels in the request context as a pointer, so a route set on any request
// copy further down the chain (gorilla/mux and chi copy the request when they match)
// is visible to the Middleware that created the slot. The mutex covers handlers that
// call SetRoute from another goroutine, e.g. under http.TimeoutHandler.
type routeSlot struct {
	mu       sync.Mutex
	set      string // from SetRoute
	captured string // from the innermost CaptureRoute whose ServeMux matched
	resolved string // final route, written by the Middleware before it calls the Observer
}

// source returns the reported route: SetRoute, else CaptureRoute, else pattern.
// The caller holds s.mu.
func (s *routeSlot) source(pattern string) string {
	if s.set != "" {
		return s.set
	}
	if s.captured != "" {
		return s.captured
	}
	return pattern
}

func noRestore() {}

// withRouteSlot prepares r for route resolution. Only metrics read the route, so without
// an Observer it returns r unchanged and a nil slot. Otherwise it returns a copy of r
// carrying an empty slot (or r itself when an outer Middleware already added one, so
// nested middlewares share a slot), and a restore func that copies the ServeMux pattern
// from the copy back onto the caller's request, so wrappers outside this Middleware still
// read r.Pattern after the handler. ServeMux's path values are unexported and cannot be
// copied back: r.PathValue stays empty outside the Middleware.
func (m *Middleware) withRouteSlot(r *http.Request) (*http.Request, *routeSlot, func()) {
	if m.metrics == nil {
		return r, nil, noRestore
	}
	if s, ok := r.Context().Value(routeKey{}).(*routeSlot); ok {
		return r, s, noRestore
	}
	s := &routeSlot{}
	cp := r.WithContext(context.WithValue(r.Context(), routeKey{}, s))
	return cp, s, func() { r.Pattern = cp.Pattern }
}

// SetRoute records the matched route for the current request, for routers that do not set
// r.Pattern (gorilla/mux, chi, …). Call it from inside the router, e.g. from a router-level
// middleware; it may be called before or after the handler runs, as long as it is before
// the request finishes.
//
// Pass a route template such as "/users/{id}", never the raw request path: every distinct
// value becomes its own metric series. SetRoute is a no-op on a request that did not pass
// through a Middleware with metrics enabled.
func SetRoute(r *http.Request, route string) {
	if s, ok := r.Context().Value(routeKey{}).(*routeSlot); ok {
		s.mu.Lock()
		s.set = route
		s.mu.Unlock()
	}
}

// CaptureRoute reports the pattern of the http.ServeMux it wraps, for setups where another
// wrapper sits between the Middleware and the mux and passes a request copy down
// (r.WithContext in an auth or tracing middleware, http.TimeoutHandler, http.MaxBytesHandler,
// http.StripPrefix). ServeMux writes r.Pattern onto the copy it receives, which the
// Middleware never sees, so without CaptureRoute such a route records as unmatched. Place it
// directly around the mux: m.Wrap(auth(middleware.CaptureRoute(mux))).
//
// When several report a route, the most specific wins: a route set with SetRoute, else the
// innermost CaptureRoute whose mux matched, else the pattern the Middleware itself sees. So
// with a mux mounted under another through http.StripPrefix, wrap the inner mux to record its
// pattern rather than the outer prefix ("/api/"). The inner pattern is relative to the
// stripped path ("GET /users/{id}", not "/api/users/{id}"). CaptureRoute reports the pattern
// even if the handler panics, and is a no-op on a request that did not pass through a
// Middleware with metrics enabled.
func CaptureRoute(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := r.Context().Value(routeKey{}).(*routeSlot)
		if !ok {
			mux.ServeHTTP(w, r)
			return
		}
		defer func() {
			if r.Pattern == "" {
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			// An inner CaptureRoute returns first: keep its more specific pattern.
			if s.captured == "" {
				s.captured = r.Pattern
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

// Route returns the route an Observer should record for r: the route the Middleware
// resolved (SetRoute, else CaptureRoute, else r.Pattern, then Cfg.RouteGroups), or "" when
// no route matched. On a request that did not pass through a Middleware it returns r.Pattern.
func Route(r *http.Request) string {
	s, ok := r.Context().Value(routeKey{}).(*routeSlot)
	if !ok {
		return r.Pattern
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Grouping never empties a route, so an empty resolved value means nothing resolved yet
	// or nothing matched; either way the raw sources give the right answer.
	if s.resolved != "" {
		return s.resolved
	}
	return s.source(r.Pattern)
}

// resolveRoute settles the route for r, after the handler has run, and stores it in the
// slot for Route to return: SetRoute, else CaptureRoute, else r.Pattern, then mapped through
// the route groups. Each nested Middleware resolves from the raw sources with its own groups.
func (m *Middleware) resolveRoute(r *http.Request, s *routeSlot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolved = m.routeGroups.apply(s.source(r.Pattern))
}

// routeGroups is a list of path prefixes, longest first, that collapse every route beneath
// them into the prefix itself.
type routeGroups []string

// newRouteGroups normalises the prefixes to clean paths: a leading method ("GET /api") is
// stripped, repeated leading and trailing slashes are dropped and one leading slash added,
// so "api", "/api/", "//api" and "GET /api" are all "/api". A bare "/" (or empty string)
// is ignored, since it would collapse every route into one.
func newRouteGroups(prefixes []string) routeGroups {
	var g routeGroups
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if i := strings.LastIndexByte(p, ' '); i >= 0 {
			p = p[i+1:]
		}
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		g = append(g, "/"+p)
	}
	slices.SortStableFunc(g, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	return g
}

// apply maps route onto its group. Any method or host before the path is kept
// ("GET /api/apple" → "GET /api"), and prefixes match whole segments only, so "/api"
// groups "/api/apple" but not "/apiary".
func (g routeGroups) apply(route string) string {
	i := strings.IndexByte(route, '/')
	if i < 0 {
		return route
	}
	head, path := route[:i], route[i:]
	for _, p := range g {
		if path == p || strings.HasPrefix(path, p+"/") {
			return head + p
		}
	}
	return route
}
