package middleware_test

import (
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
// already-sent status, matching net/http, which does not write to a started response.
func TestPanicRecover_NoBodyAppendedAfterStart(t *testing.T) {
	buf, logger := newMemSlog()
	m := middleware.New(middleware.Cfg{PanicRecover: true, Logger: logger})
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial-"))
		panic("boom after partial write")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))

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
