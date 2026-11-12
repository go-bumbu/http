package middleware_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-bumbu/http/middleware"
)

type spyObserver struct {
	calls  int
	status int
	dur    time.Duration
}

func (s *spyObserver) Observe(status int, _ *http.Request, d time.Duration) {
	s.calls++
	s.status = status
	s.dur = d
}

// TestCombinedMiddleware_CallsObserver proves the combined middleware records
// metrics through the Observer seam — no Prometheus involved.
func TestCombinedMiddleware_CallsObserver(t *testing.T) {
	spy := &spyObserver{}
	m := middleware.New(middleware.Cfg{Metrics: spy})
	h := m.Wrap(testHandler(503, "boom"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	if spy.calls != 1 {
		t.Fatalf("Observe called %d times, want 1", spy.calls)
	}
	if spy.status != 503 {
		t.Errorf("observed status = %d, want 503", spy.status)
	}
	if spy.dur < 0 {
		t.Errorf("observed duration = %v, want >= 0", spy.dur)
	}
}

// TestCombinedMiddleware_ObservesOncePerPath is the regression test for requests that went
// unrecorded on a panic: with PanicRecover off any panic skipped both the access log and the
// Observer, and with it on a handler's own http.ErrAbortHandler was observed but not logged.
// Every path must log one access line and call Observe exactly once, and a panic the
// middleware does not turn into a 500 must still reach the caller.
func TestCombinedMiddleware_ObservesOncePerPath(t *testing.T) {
	errBoom := errors.New("boom")
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(errBoom) })
	abort := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	startedBoom := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic(errBoom)
	})

	tcs := []struct {
		name      string
		recover   bool
		handler   http.Handler
		wantPanic error // nil: the request must complete normally
	}{
		{name: "normal, recovery on", recover: true, handler: ok},
		{name: "normal, recovery off", recover: false, handler: ok},
		{name: "recovered 500", recover: true, handler: boom},
		{name: "started then panic, recovery on", recover: true, handler: startedBoom, wantPanic: http.ErrAbortHandler},
		{name: "ErrAbortHandler, recovery on", recover: true, handler: abort, wantPanic: http.ErrAbortHandler},
		{name: "panic, recovery off", recover: false, handler: boom, wantPanic: errBoom},
		{name: "ErrAbortHandler, recovery off", recover: false, handler: abort, wantPanic: http.ErrAbortHandler},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			buf, logger := newMemSlog()
			spy := &spyObserver{}
			h := middleware.New(middleware.Cfg{PanicRecover: tc.recover, Logger: logger, Metrics: spy}).Wrap(tc.handler)

			var got any
			func() {
				defer func() { got = recover() }()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
			}()

			if tc.wantPanic == nil && got != nil {
				t.Errorf("unexpected panic %v", got)
			}
			if tc.wantPanic != nil && got != tc.wantPanic {
				t.Errorf("panic = %v, want %v", got, tc.wantPanic)
			}
			if spy.calls != 1 {
				t.Errorf("Observe called %d times, want 1", spy.calls)
			}
			if n := strings.Count(buf.String(), "response-code="); n != 1 {
				t.Errorf("access log lines = %d, want 1; log %q", n, buf.String())
			}
		})
	}
}
