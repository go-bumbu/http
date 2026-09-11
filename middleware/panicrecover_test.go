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

	handler := m.Middleware(panicHandler())

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
