package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatWriter_WriteHeader_OnlyOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, false, false)

	sw.WriteHeader(http.StatusNotFound)
	sw.WriteHeader(http.StatusOK)

	if sw.StatusCode() != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, sw.StatusCode())
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected recorder status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestStatWriter_DefaultStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, false, false)

	if sw.StatusCode() != http.StatusOK {
		t.Errorf("expected default status %d, got %d", http.StatusOK, sw.StatusCode())
	}
}

func TestStatWriter_Write_PassthroughOnSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, false)

	sw.WriteHeader(http.StatusOK)
	n, err := sw.Write([]byte("hello"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected n=5, got %d", n)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("expected body 'hello', got %q", rec.Body.String())
	}
}

func TestStatWriter_Write_BuffersOnError(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, false)

	sw.WriteHeader(http.StatusInternalServerError)
	n, err := sw.Write([]byte("db broke"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 8 {
		t.Errorf("expected n=8, got %d", n)
	}
	// Body should NOT be written to recorder (buffered only)
	if rec.Body.Len() != 0 {
		t.Errorf("expected empty recorder body, got %q", rec.Body.String())
	}
	// Buffer should have the content
	if sw.buf.String() != "db broke" {
		t.Errorf("expected buffer 'db broke', got %q", sw.buf.String())
	}
}

func TestStatWriter_Write_TeeOnError(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, true)

	sw.WriteHeader(http.StatusBadGateway)
	n, err := sw.Write([]byte("upstream error"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 14 {
		t.Errorf("expected n=14, got %d", n)
	}
	// Body should be written to both recorder and buffer
	if rec.Body.String() != "upstream error" {
		t.Errorf("expected recorder body 'upstream error', got %q", rec.Body.String())
	}
	if !sw.BodyForwarded() {
		t.Error("expected BodyForwarded() to be true")
	}
}

func TestStatWriter_BufferOverflow(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, true)
	sw.WriteHeader(http.StatusInternalServerError)

	// Write more than 2000 bytes
	bigBody := strings.Repeat("x", 2500)
	n, err := sw.Write([]byte(bigBody))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Tee mode: full body should be forwarded to client regardless of buffer limit
	if n != 2500 {
		t.Errorf("expected n=2500, got %d", n)
	}
	if rec.Body.Len() != 2500 {
		t.Errorf("expected recorder body len 2500, got %d", rec.Body.Len())
	}
	// Buffer should be truncated
	if !sw.buf.Truncated() {
		t.Error("expected buffer to be truncated")
	}
	if sw.buf.Len() != 2000 {
		t.Errorf("expected buffer len 2000, got %d", sw.buf.Len())
	}
}

func TestStatWriter_DeferredHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, false)

	sw.WriteHeader(http.StatusBadRequest)

	// Header should be deferred (not yet written to recorder)
	// httptest.ResponseRecorder defaults Code to 200, only changes on explicit WriteHeader
	if sw.headerWritten {
		t.Error("expected header to be deferred")
	}

	sw.flushHeader()
	if !sw.headerWritten {
		t.Error("expected header to be written after flushHeader")
	}
}

func TestStatWriter_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, false, false)

	if sw.Unwrap() != rec {
		t.Error("Unwrap should return the underlying ResponseWriter")
	}
}

func TestStatWriter_StatusCodeStr(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, false, false)
	sw.WriteHeader(http.StatusTeapot)

	if sw.StatusCodeStr() != "418" {
		t.Errorf("expected '418', got %q", sw.StatusCodeStr())
	}
}
