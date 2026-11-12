package middleware_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-bumbu/http/middleware"
)

func panicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went terribly wrong")
	})
}

func TestPanicRecover_BundledMiddleware(t *testing.T) {
	buf, logger := newMemSlog()
	m := middleware.New(middleware.Cfg{
		PanicRecover: true,
		Logger:       logger,
	})

	handler := m.Wrap(panicHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/boom", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body != http.StatusText(http.StatusInternalServerError) {
		t.Errorf("expected generic status text body, got %q", body)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "something went terribly wrong") {
		t.Errorf("expected panic in log, got %q", logOutput)
	}
}

// TestPanicRecover_NoBodyAppendedAfterStart is the regression test for a panic that fires
// after the handler has already committed the response — written the header and part of the
// body — without flushing. Recovery must not append "Internal Server Error" under the
// already-sent status, matching net/http, which does not write to a started response. Instead
// recovery re-panics with http.ErrAbortHandler (see TestPanicRecover_StartedResponseAborts).
func TestPanicRecover_NoBodyAppendedAfterStart(t *testing.T) {
	buf, logger := newMemSlog()
	m := middleware.New(middleware.Cfg{PanicRecover: true, Logger: logger})
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial-"))
		panic("boom after partial write")
	}))

	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Errorf("expected a re-panic with http.ErrAbortHandler, got %v", got)
			}
		}()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	}()

	if rec.Code != http.StatusOK {
		t.Errorf("committed status 200 must stand, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != "partial-" {
		t.Errorf("recovery must not append a body to a started response, got %q", got)
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Errorf("panic must still be logged, got %q", buf.String())
	}
}

// TestPanicRecover_StartedResponseAborts is the regression test for a recovered panic after the
// response started: finishing it normally gave the client a clean EOF, so a truncated body
// looked complete. Recovery must abort the connection, as net/http does without recovery, while
// still logging the panic and recording the metric.
func TestPanicRecover_StartedResponseAborts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flush bool
	}{
		{name: "flushed", flush: true},
		{name: "unflushed", flush: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf, logger := newMemSlog()
			spy := &routeSpy{}
			m := middleware.New(middleware.Cfg{PanicRecover: true, Logger: logger, Metrics: spy})
			srv := httptest.NewServer(m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"items":[`))
				if tc.flush {
					_ = http.NewResponseController(w).Flush()
				}
				panic("boom mid-body")
			})))
			defer srv.Close()

			resp, err := http.Get(srv.URL)
			var readErr error
			if err == nil {
				_, readErr = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			if err == nil && readErr == nil {
				t.Error("expected the client to detect the truncation, got a clean EOF")
			}
			srv.Close() // waits for the handler, so its log and metric are complete
			if !strings.Contains(buf.String(), "panic recovered") {
				t.Errorf("panic must still be logged, got %q", buf.String())
			}
			if spy.calls != 1 {
				t.Errorf("expected one metric observation, got %d", spy.calls)
			}
		})
	}
}
