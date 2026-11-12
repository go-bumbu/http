package middleware_test

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
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
//
// It also pins the recorded status: a request aborted before any status was committed is
// recorded as a 500 at ERROR (the client gets a reset connection, not the StatWriter's
// default 200), while one aborted after committing a status keeps that status.
func TestCombinedMiddleware_ObservesOncePerPath(t *testing.T) {
	errBoom := errors.New("boom")
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(errBoom) })
	abort := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	startedBoom := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic(errBoom)
	})
	flushedBoom := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		panic(errBoom)
	})
	hijackedBoom := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
		panic(errBoom)
	})

	tcs := []struct {
		name       string
		recover    bool
		handler    http.Handler
		wantPanic  error // nil: the request must complete normally
		wantStatus int
		wantLevel  string
	}{
		{name: "normal, recovery on", recover: true, handler: ok, wantStatus: 200, wantLevel: "INFO"},
		{name: "normal, recovery off", recover: false, handler: ok, wantStatus: 200, wantLevel: "INFO"},
		{name: "recovered 500", recover: true, handler: boom, wantStatus: 500, wantLevel: "ERROR"},
		{name: "started then panic, recovery on", recover: true, handler: startedBoom, wantPanic: http.ErrAbortHandler, wantStatus: 200, wantLevel: "INFO"},
		{name: "started then panic, recovery off", recover: false, handler: startedBoom, wantPanic: errBoom, wantStatus: 200, wantLevel: "INFO"},
		{name: "flushed then panic, recovery on", recover: true, handler: flushedBoom, wantPanic: http.ErrAbortHandler, wantStatus: 200, wantLevel: "INFO"},
		{name: "flushed then panic, recovery off", recover: false, handler: flushedBoom, wantPanic: errBoom, wantStatus: 200, wantLevel: "INFO"},
		{name: "hijacked then panic, recovery on", recover: true, handler: hijackedBoom, wantPanic: http.ErrAbortHandler, wantStatus: 101, wantLevel: "INFO"},
		{name: "hijacked then panic, recovery off", recover: false, handler: hijackedBoom, wantPanic: errBoom, wantStatus: 101, wantLevel: "INFO"},
		{name: "ErrAbortHandler, recovery on", recover: true, handler: abort, wantPanic: http.ErrAbortHandler, wantStatus: 500, wantLevel: "ERROR"},
		{name: "panic, recovery off", recover: false, handler: boom, wantPanic: errBoom, wantStatus: 500, wantLevel: "ERROR"},
		{name: "ErrAbortHandler, recovery off", recover: false, handler: abort, wantPanic: http.ErrAbortHandler, wantStatus: 500, wantLevel: "ERROR"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			buf, logger := newMemSlog()
			spy := &spyObserver{}
			h := middleware.New(middleware.Cfg{PanicRecover: tc.recover, Logger: logger, Metrics: spy}).Wrap(tc.handler)

			var got any
			func() {
				defer func() { got = recover() }()
				h.ServeHTTP(&hijackRecorder{ResponseRecorder: httptest.NewRecorder()}, httptest.NewRequest("GET", "/x", nil))
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
			if spy.status != tc.wantStatus {
				t.Errorf("observed status = %d, want %d", spy.status, tc.wantStatus)
			}
			if n := strings.Count(buf.String(), "response-code="); n != 1 {
				t.Errorf("access log lines = %d, want 1; log %q", n, buf.String())
			}
			wantLine := fmt.Sprintf("%s method=GET url=/x response-code=%d ", tc.wantLevel, tc.wantStatus)
			if !strings.Contains(buf.String(), wantLine) {
				t.Errorf("access log missing %q; log %q", wantLine, buf.String())
			}
		})
	}
}

// hijackRecorder is an httptest.ResponseRecorder that can also be hijacked, handing out one
// end of a net.Pipe so the hijack succeeds without a real server.
type hijackRecorder struct {
	*httptest.ResponseRecorder
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	server, client := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

// TestCombinedMiddleware_GoexitRecordedAs500 pins the runtime.Goexit path (e.g. t.FailNow in a
// handler): net/http closes the connection without a response, so a request that committed no
// status is recorded as a 500 at ERROR rather than the StatWriter's default 200.
func TestCombinedMiddleware_GoexitRecordedAs500(t *testing.T) {
	for _, recoverOn := range []bool{true, false} {
		t.Run(fmt.Sprintf("recover=%v", recoverOn), func(t *testing.T) {
			buf, logger := newMemSlog()
			spy := &spyObserver{}
			h := middleware.New(middleware.Cfg{PanicRecover: recoverOn, Logger: logger, Metrics: spy}).
				Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtime.Goexit() }))

			done := make(chan struct{})
			go func() {
				defer close(done)
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
			}()
			<-done

			if spy.calls != 1 || spy.status != http.StatusInternalServerError {
				t.Errorf("Observe calls = %d status = %d, want 1 call with 500", spy.calls, spy.status)
			}
			if want := "ERROR method=GET url=/x response-code=500 "; !strings.Contains(buf.String(), want) {
				t.Errorf("access log missing %q; log %q", want, buf.String())
			}
		})
	}
}
