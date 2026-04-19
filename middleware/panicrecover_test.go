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

func TestPanicRecover_Returns500(t *testing.T) {
	_, logger := newMemSlog()
	handler := middleware.PanicRecover(logger)(panicHandler())

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/test")
	if err != nil {
		t.Fatalf("expected a valid response, got error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Internal Server Error") {
		t.Errorf("expected generic error body, got %q", string(body))
	}
}

func TestPanicRecover_LogsPanic(t *testing.T) {
	buf, logger := newMemSlog()
	handler := middleware.PanicRecover(logger)(panicHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/boom", nil)
	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "something went terribly wrong") {
		t.Errorf("expected panic message in log, got %q", logOutput)
	}
}

func TestPanicRecover_ServerSurvives(t *testing.T) {
	_, logger := newMemSlog()
	mux := http.NewServeMux()
	mux.Handle("/panic", panicHandler())
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("alive"))
	})

	handler := middleware.PanicRecover(logger)(mux)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// First request panics
	resp, err := http.Get(srv.URL + "/panic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp.Body.Close()

	// Second request should work fine
	resp, err = http.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatalf("server died after panic: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "alive" {
		t.Errorf("expected 'alive', got %q", string(body))
	}
}

func TestPanicRecover_NilLogger(t *testing.T) {
	handler := middleware.PanicRecover(nil)(panicHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/boom", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestPanicRecover_NoPanic(t *testing.T) {
	_, logger := newMemSlog()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	handler := middleware.PanicRecover(logger)(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fine", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("expected 'ok', got %q", rec.Body.String())
	}
}

func TestPanicRecover_BundledMiddlewareWithJSON(t *testing.T) {
	buf, logger := newMemSlog()
	m := middleware.New(middleware.Cfg{
		JsonErrors:   true,
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
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, `"code":500`) {
		t.Errorf("expected JSON error envelope, got %q", body)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "something went terribly wrong") {
		t.Errorf("expected panic in log, got %q", logOutput)
	}
}

func TestPanicRecover_ComposesWithJSONErrors(t *testing.T) {
	_, logger := newMemSlog()
	// JSONErrors wraps PanicRecover so the 500 from recovery gets intercepted as JSON.
	handler := middleware.JSONErrors(false)(
		middleware.PanicRecover(logger)(panicHandler()),
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/boom", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, `"code":500`) {
		t.Errorf("expected JSON error envelope, got %q", body)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json content-type, got %q", ct)
	}
}

