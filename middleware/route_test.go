package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-bumbu/http/middleware"
)

// routeSpy records the route the Observer sees via middleware.Route.
type routeSpy struct {
	calls int
	route string
}

func (s *routeSpy) Observe(_ int, r *http.Request, _ time.Duration) {
	s.calls++
	s.route = middleware.Route(r)
}

// serve wraps h with a Middleware built from cfg (Metrics set to a fresh spy),
// serves one request to target, and returns the route the observer saw.
func serve(t *testing.T, cfg middleware.Cfg, h http.Handler, target string) string {
	t.Helper()
	spy := &routeSpy{}
	cfg.Metrics = spy
	middleware.New(cfg).Wrap(h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
	if spy.calls != 1 {
		t.Fatalf("Observe called %d times, want 1", spy.calls)
	}
	return spy.route
}

// setRoute returns a handler that reports route via SetRoute, then writes 200.
func setRoute(route string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		middleware.SetRoute(r, route)
		w.WriteHeader(200)
	})
}

// TestRoute_ServeMuxPattern is the regression guard for the route slot: the middleware
// passes a request copy down the chain, and ServeMux writes r.Pattern onto that copy, so
// the observer must be handed the copy — otherwise every stdlib route reads as empty.
func TestRoute_ServeMuxPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", testHandler(200, "ok"))

	if got := serve(t, middleware.Cfg{}, mux, "/users/42"); got != "GET /users/{id}" {
		t.Errorf("route = %q, want %q", got, "GET /users/{id}")
	}
	if got := serve(t, middleware.Cfg{}, mux, "/nope"); got != "" {
		t.Errorf("unmatched route = %q, want empty", got)
	}
}

// TestRoute_SetRouteBeatsPattern checks the resolution order: SetRoute, then r.Pattern.
func TestRoute_SetRouteBeatsPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /p", setRoute("/explicit"))
	if got := serve(t, middleware.Cfg{}, mux, "/p"); got != "/explicit" {
		t.Errorf("route = %q, want %q", got, "/explicit")
	}
}

// TestRoute_OuterSeesPattern guards wrappers outside the Middleware (tracing, access logs)
// that read r.Pattern after the handler: the Middleware passes a copy down, so it must hand
// the ServeMux pattern back to the caller's request — also when a panic unwinds.
func TestRoute_OuterSeesPattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /u/{id}", testHandler(200, "ok"))
	mux.Handle("GET /abort", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	tcs := []struct {
		name   string
		cfg    middleware.Cfg
		target string
		want   string
	}{
		{name: "metrics off", cfg: middleware.Cfg{}, target: "/u/1", want: "GET /u/{id}"},
		{name: "metrics on", cfg: middleware.Cfg{Metrics: &routeSpy{}}, target: "/u/1", want: "GET /u/{id}"},
		{name: "abort panic", cfg: middleware.Cfg{Metrics: &routeSpy{}, PanicRecover: true}, target: "/abort", want: "GET /abort"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			h := middleware.New(tc.cfg).Wrap(mux)
			r := httptest.NewRequest("GET", tc.target, nil)
			func() {
				defer func() { _ = recover() }() // the abort case re-panics by design
				h.ServeHTTP(httptest.NewRecorder(), r)
			}()
			if r.Pattern != tc.want {
				t.Errorf("outer r.Pattern = %q, want %q", r.Pattern, tc.want)
			}
		})
	}
}

// TestRoute_NoCopyWithoutMetrics checks the route slot (and its request copy) is only paid
// for when an Observer is configured.
func TestRoute_NoCopyWithoutMetrics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      middleware.Cfg
		wantSame bool
	}{
		{name: "metrics off", cfg: middleware.Cfg{}, wantSame: true},
		{name: "metrics on", cfg: middleware.Cfg{Metrics: &routeSpy{}}, wantSame: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *http.Request
			h := middleware.New(tc.cfg).Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r
			}))
			r := httptest.NewRequest("GET", "/", nil)
			h.ServeHTTP(httptest.NewRecorder(), r)
			if (got == r) != tc.wantSame {
				t.Errorf("handler got the caller's request = %v, want %v", got == r, tc.wantSame)
			}
		})
	}
}

type routerKey struct{}

// fakeRouter mimics gorilla/mux: on a match it stores the route template on a request
// copy's context and calls next with that copy.
func fakeRouter(template string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routerKey{}, template)))
	})
}

// routeFromRouter mimics the app's glue: a router-level middleware that reads the match
// (mux.CurrentRoute in gorilla) and reports it with SetRoute.
func routeFromRouter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, _ := r.Context().Value(routerKey{}).(string)
		middleware.SetRoute(r, route)
		next.ServeHTTP(w, r)
	})
}

// TestRoute_RouterUseMode mimics gorilla's router.Use(m.Wrap, routeFromRouter): the router
// matches first, then runs the Middleware, then the glue inside it.
func TestRoute_RouterUseMode(t *testing.T) {
	spy := &routeSpy{}
	m := middleware.New(middleware.Cfg{Metrics: spy})
	h := fakeRouter("/users/{id}", m.Wrap(routeFromRouter(testHandler(200, "ok"))))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/1", nil))
	if spy.route != "/users/{id}" {
		t.Errorf("route = %q, want %q", spy.route, "/users/{id}")
	}
}

// TestRoute_WrapWholeRouter mimics m.Wrap(router) with the glue inside the router: the
// router copies the request, and SetRoute on the copy must still reach the Middleware.
func TestRoute_WrapWholeRouter(t *testing.T) {
	router := fakeRouter("/users/{id}", routeFromRouter(testHandler(200, "ok")))
	if got := serve(t, middleware.Cfg{}, router, "/users/1"); got != "/users/{id}" {
		t.Errorf("route = %q, want %q", got, "/users/{id}")
	}
}

// TestRoute_Nested checks that nested middlewares share one slot, each resolving the route
// from the raw sources with its own groups, and that an inner Middleware with metrics works
// under an outer one without.
func TestRoute_Nested(t *testing.T) {
	api := []string{"/api"}
	tcs := []struct {
		name                 string
		outer, inner         middleware.Cfg
		wantOuter, wantInner string
	}{
		{name: "outer groups", outer: middleware.Cfg{RouteGroups: api}, wantOuter: "/api", wantInner: "/api/items/{id}"},
		{name: "inner groups", inner: middleware.Cfg{RouteGroups: api}, wantOuter: "/api/items/{id}", wantInner: "/api"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			outer, inner := &routeSpy{}, &routeSpy{}
			tc.outer.Metrics, tc.inner.Metrics = outer, inner
			h := middleware.New(tc.outer).Wrap(middleware.New(tc.inner).Wrap(setRoute("/api/items/{id}")))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/items/1", nil))
			if outer.route != tc.wantOuter {
				t.Errorf("outer route = %q, want %q", outer.route, tc.wantOuter)
			}
			if inner.route != tc.wantInner {
				t.Errorf("inner route = %q, want %q", inner.route, tc.wantInner)
			}
		})
	}

	t.Run("outer without metrics", func(t *testing.T) {
		inner := &routeSpy{}
		h := middleware.New(middleware.Cfg{}).Wrap(
			middleware.New(middleware.Cfg{Metrics: inner}).Wrap(setRoute("/x/{id}")))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x/1", nil))
		if inner.route != "/x/{id}" {
			t.Errorf("inner route = %q, want %q", inner.route, "/x/{id}")
		}
	})
}

// TestRoute_Panic checks the route is resolved when the observer runs from panic recovery.
func TestRoute_Panic(t *testing.T) {
	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		middleware.SetRoute(r, "/boom")
		panic("boom")
	})
	if got := serve(t, middleware.Cfg{PanicRecover: true}, h, "/boom"); got != "/boom" {
		t.Errorf("route = %q, want %q", got, "/boom")
	}
}

// TestRoute_AbortPanic checks the route is resolved on the http.ErrAbortHandler path,
// where the Middleware records the metric and then re-panics.
func TestRoute_AbortPanic(t *testing.T) {
	spy := &routeSpy{}
	h := middleware.New(middleware.Cfg{Metrics: spy, PanicRecover: true}).Wrap(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			middleware.SetRoute(r, "/abort")
			panic(http.ErrAbortHandler)
		}))

	func() {
		defer func() {
			rec := recover()
			if err, ok := rec.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/abort", nil))
	}()
	if spy.calls != 1 || spy.route != "/abort" {
		t.Errorf("calls = %d, route = %q, want 1 and %q", spy.calls, spy.route, "/abort")
	}
}

// TestRoute_SetRouteAfterTimeout pins down why the slot has a mutex: under
// http.TimeoutHandler the handler goroutine can call SetRoute while the Middleware is
// resolving the route. Nothing orders the two accesses, so without the lock -race reports
// them; the resolved route itself depends on timing and is not asserted.
func TestRoute_SetRouteAfterTimeout(t *testing.T) {
	done := make(chan struct{})
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer close(done)
		time.Sleep(5 * time.Millisecond)
		middleware.SetRoute(r, "/late")
	})
	h := middleware.New(middleware.Cfg{Metrics: &routeSpy{}}).Wrap(http.TimeoutHandler(slow, time.Millisecond, "timeout"))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/slow", nil))
	<-done

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestRoute_OutsideMiddleware: SetRoute is a no-op and Route falls back to r.Pattern.
func TestRoute_OutsideMiddleware(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	middleware.SetRoute(r, "/ignored")
	if got := middleware.Route(r); got != "" {
		t.Errorf("route = %q, want empty", got)
	}
	r.Pattern = "/x"
	if got := middleware.Route(r); got != "/x" {
		t.Errorf("route = %q, want %q", got, "/x")
	}
}

func TestRoute_Groups(t *testing.T) {
	tcs := []struct {
		name   string
		groups []string
		route  string
		want   string
	}{
		{name: "groups a sub-route", groups: []string{"/api"}, route: "/api/banana", want: "/api"},
		{name: "keeps the method", groups: []string{"/api"}, route: "GET /api/apple/{id}", want: "GET /api"},
		{name: "keeps a host", groups: []string{"/api"}, route: "GET example.com/api/x", want: "GET example.com/api"},
		{name: "exact match", groups: []string{"/api"}, route: "/api", want: "/api"},
		{name: "subtree pattern", groups: []string{"/api"}, route: "/api/", want: "/api"},
		{name: "whole segments only", groups: []string{"/api"}, route: "/apiary", want: "/apiary"},
		{name: "longest prefix wins", groups: []string{"/api", "/api/admin"}, route: "/api/admin/users", want: "/api/admin"},
		{name: "missing and trailing slash", groups: []string{"api/"}, route: "/api/x", want: "/api"},
		{name: "repeated slashes", groups: []string{"//api//"}, route: "/api/x", want: "/api"},
		{name: "method stripped from prefix", groups: []string{"GET /api"}, route: "POST /api/x", want: "POST /api"},
		{name: "bare slash ignored", groups: []string{"/"}, route: "/x/y", want: "/x/y"},
		{name: "no route stays empty", groups: []string{"/api"}, route: "", want: ""},
		{name: "unrelated route untouched", groups: []string{"/api"}, route: "/home", want: "/home"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			if got := serve(t, middleware.Cfg{RouteGroups: tc.groups}, setRoute(tc.route), "/"); got != tc.want {
				t.Errorf("route = %q, want %q", got, tc.want)
			}
		})
	}
}

type nopObserver struct{}

func (nopObserver) Observe(int, *http.Request, time.Duration) {}

// BenchmarkWrap measures the per-request cost of the Middleware with and without metrics
// (the route slot and request copy are only added with metrics).
func BenchmarkWrap(b *testing.B) {
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", testHandler(200, "ok"))
	for _, bc := range []struct {
		name string
		cfg  middleware.Cfg
	}{
		{name: "no metrics", cfg: middleware.Cfg{}},
		{name: "metrics", cfg: middleware.Cfg{Metrics: nopObserver{}}},
		{name: "metrics+groups", cfg: middleware.Cfg{Metrics: nopObserver{}, RouteGroups: []string{"/users"}}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			h := middleware.New(bc.cfg).Wrap(mux)
			r := httptest.NewRequest("GET", "/users/1", nil)
			w := httptest.NewRecorder()
			b.ReportAllocs()
			for b.Loop() {
				h.ServeHTTP(w, r)
			}
		})
	}
}
