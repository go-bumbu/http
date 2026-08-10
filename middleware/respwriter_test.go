package middleware

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
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

// countingRW mimics net/http's behaviour of implicitly calling WriteHeader(200)
// on the first Write, and counts how many times WriteHeader reaches the writer.
// httptest.ResponseRecorder cannot be used here because it does not surface the
// duplicate-WriteHeader condition that the real net/http server logs as
// "superfluous response.WriteHeader call".
type countingRW struct {
	header           http.Header
	writeHeaderCalls int
	wroteHeader      bool
}

func (c *countingRW) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}

func (c *countingRW) WriteHeader(int) {
	c.writeHeaderCalls++
	c.wroteHeader = true
}

func (c *countingRW) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return len(b), nil
}

// TestStatWriter_NoSuperfluousWriteHeader guards against a regression where a
// successful handler that writes a body without an explicit WriteHeader caused
// flushHeader to issue a second WriteHeader on the underlying writer (logged by
// net/http as "superfluous response.WriteHeader call"). The implicit header
// commit during Write must be recorded so flushHeader becomes a no-op.
func TestStatWriter_NoSuperfluousWriteHeader(t *testing.T) {
	crw := &countingRW{}
	sw := NewWriter(crw, true, true) // matches the Logging middleware config

	// Success handler: writes body without an explicit WriteHeader call.
	_, _ = sw.Write([]byte("hello"))
	// Logging middleware flushes the header after the handler returns.
	sw.flushHeader()

	if crw.writeHeaderCalls != 1 {
		t.Errorf("expected exactly 1 WriteHeader call, got %d (superfluous call)", crw.writeHeaderCalls)
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

// TestStatWriter_ImplementsFlusher guards the pre-Go1.20 idiom: handlers and nested
// middleware that type-assert w.(http.Flusher) instead of using http.ResponseController
// must still be able to stream through StatWriter.
func TestStatWriter_ImplementsFlusher(t *testing.T) {
	sw := NewWriter(httptest.NewRecorder(), true, true)

	if _, ok := interface{}(sw).(http.Flusher); !ok {
		t.Error("StatWriter must implement http.Flusher for legacy type assertions")
	}
	if _, ok := interface{}(sw).(http.Hijacker); !ok {
		t.Error("StatWriter must implement http.Hijacker for legacy type assertions")
	}
}

// TestStatWriter_FlushErrorUnsupported verifies that flushing a writer that cannot flush
// reports ErrNotSupported (as http.ResponseController does) and leaves interception intact,
// so the middleware can still replace the body.
func TestStatWriter_FlushErrorUnsupported(t *testing.T) {
	// countingRW implements neither Flush nor FlushError.
	sw := NewWriter(&countingRW{}, true, false)
	sw.WriteHeader(http.StatusBadGateway)

	err := sw.FlushError()
	if !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", err)
	}
	if sw.Streaming() {
		t.Error("a failed flush must not release body interception")
	}
	if !sw.canReplaceBody() {
		t.Error("middleware must still be able to replace the body after a failed flush")
	}
}

// TestStatWriter_FlushCommitsDeferredStatus is the regression test for the status-code
// downgrade: with the header deferred (teeOnErr false), a flush used to unwrap past
// StatWriter and implicitly commit WriteHeader(200), discarding the real status code.
func TestStatWriter_FlushCommitsDeferredStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, false)

	sw.WriteHeader(http.StatusServiceUnavailable)
	if sw.headerWritten {
		t.Fatal("precondition: header should be deferred")
	}

	sw.Flush()

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d committed on flush, got %d",
			http.StatusServiceUnavailable, rec.Code)
	}
	if !sw.Streaming() {
		t.Error("expected Streaming() to be true after a flush")
	}
}

// TestStatWriter_FlushForwardsBufferedBody verifies that bytes written before the first
// flush are not lost: they are forwarded to the client once interception is released.
func TestStatWriter_FlushForwardsBufferedBody(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewWriter(rec, true, false)

	sw.WriteHeader(http.StatusServiceUnavailable)
	_, _ = sw.Write([]byte("first chunk"))
	if rec.Body.Len() != 0 {
		t.Fatal("precondition: body should be buffered before the flush")
	}

	sw.Flush()

	if rec.Body.String() != "first chunk" {
		t.Errorf("expected buffered body forwarded, got %q", rec.Body.String())
	}
	// The buffer must stay readable so the log still gets the error message.
	if sw.buf.String() != "first chunk" {
		t.Errorf("expected buffer preserved for logging, got %q", sw.buf.String())
	}
	// Subsequent writes go straight through.
	_, _ = sw.Write([]byte("|second"))
	if rec.Body.String() != "first chunk|second" {
		t.Errorf("expected passthrough after flush, got %q", rec.Body.String())
	}
	if sw.canReplaceBody() {
		t.Error("middleware must not replace the body of a streamed response")
	}
}

// TestStatWriter_HijackSuppressesHeaderWrite verifies that after the handler takes over the
// connection, flushHeader does not write a status code (which net/http logs as
// "response.WriteHeader on hijacked connection" and which would corrupt the raw response).
func TestStatWriter_HijackSuppressesHeaderWrite(t *testing.T) {
	hw := &hijackableRW{countingRW: countingRW{}}
	sw := NewWriter(hw, true, true)

	if _, _, err := sw.Hijack(); err != nil {
		t.Fatalf("unexpected hijack error: %v", err)
	}
	sw.flushHeader()

	if hw.writeHeaderCalls != 0 {
		t.Errorf("expected no WriteHeader after hijack, got %d calls", hw.writeHeaderCalls)
	}
	if sw.canReplaceBody() {
		t.Error("middleware must not write a body after a hijack")
	}
}

// hijackableRW is a countingRW that also supports hijacking, returning a connection
// backed by a net.Pipe so the call succeeds without a real server.
type hijackableRW struct {
	countingRW
	conn net.Conn
}

func (h *hijackableRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	server, client := net.Pipe()
	_ = client.Close()
	h.conn = server
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

// --- end-to-end streaming tests against a real net/http server ---

// streamHandler writes n SSE chunks, flushing between each via the given flush strategy.
func streamHandler(t *testing.T, status int, chunks int, legacyAssert bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		for i := 0; i < chunks; i++ {
			if _, err := fmt.Fprintf(w, "data: chunk-%d\n\n", i); err != nil {
				t.Errorf("write chunk %d: %v", i, err)
				return
			}
			if legacyAssert {
				f, ok := w.(http.Flusher)
				if !ok {
					t.Error("handler could not obtain http.Flusher via type assertion")
					return
				}
				f.Flush()
			} else if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("ResponseController.Flush: %v", err)
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
}

// readStreamed reads the response and reports the status plus how many separate reads
// the body arrived in. A buffered (non-streaming) response arrives in a single read.
func readStreamed(t *testing.T, addr string) (status int, reads int, body string) {
	t.Helper()
	//nolint:gosec // G107: addr is the URL of a test-local httptest server
	resp, err := http.Get(addr)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()

	buf := make([]byte, 4096)
	var sb strings.Builder
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			reads++
			sb.Write(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Logf("read ended with: %v", err)
			}
			break
		}
	}
	return resp.StatusCode, reads, sb.String()
}

// TestStreaming_EndToEnd covers streaming through the full middleware for both flush
// styles, both middleware configurations, and success as well as error statuses.
// Before the fix, the legacy-assertion cases could not flush at all and the error-status
// cases with body interception were collapsed into a single replaced body.
func TestStreaming_EndToEnd(t *testing.T) {
	tests := []struct {
		name         string
		cfg          Cfg
		status       int
		legacyAssert bool
	}{
		{"responseController/tee/200", Cfg{}, http.StatusOK, false},
		{"responseController/tee/503", Cfg{}, http.StatusServiceUnavailable, false},
		{"responseController/jsonErrors/200", Cfg{JsonErrors: true}, http.StatusOK, false},
		{"responseController/jsonErrors/503", Cfg{JsonErrors: true}, http.StatusServiceUnavailable, false},
		{"responseController/genericErrs/503", Cfg{GenericErrs: true}, http.StatusServiceUnavailable, false},
		{"legacyAssert/tee/200", Cfg{}, http.StatusOK, true},
		{"legacyAssert/tee/503", Cfg{}, http.StatusServiceUnavailable, true},
		{"legacyAssert/jsonErrors/503", Cfg{JsonErrors: true}, http.StatusServiceUnavailable, true},
	}

	const chunks = 3
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.cfg)
			srv := httptest.NewServer(m.Middleware(streamHandler(t, tc.status, chunks, tc.legacyAssert)))
			defer srv.Close()

			status, reads, body := readStreamed(t, srv.URL)

			if status != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, status)
			}
			if reads < chunks {
				t.Errorf("expected at least %d incremental reads (streamed), got %d — body was buffered",
					chunks, reads)
			}
			for i := 0; i < chunks; i++ {
				want := fmt.Sprintf("data: chunk-%d", i)
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q; got %q", want, body)
				}
			}
			if strings.Contains(body, `"error"`) {
				t.Errorf("streamed body must not be replaced by an error envelope: %q", body)
			}
		})
	}
}

// TestStreaming_ThroughReverseProxy verifies the case from the original report: the
// middleware in front of a httputil.ReverseProxy whose upstream streams.
func TestStreaming_ThroughReverseProxy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    Cfg
		status int
	}{
		{"tee/200", Cfg{}, http.StatusOK},
		{"jsonErrors/200", Cfg{JsonErrors: true}, http.StatusOK},
		{"jsonErrors/503", Cfg{JsonErrors: true}, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const chunks = 3
			upstream := httptest.NewServer(streamHandler(t, tc.status, chunks, false))
			defer upstream.Close()

			u, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			m := New(tc.cfg)
			front := httptest.NewServer(m.Middleware(httputil.NewSingleHostReverseProxy(u)))
			defer front.Close()

			status, reads, body := readStreamed(t, front.URL)

			if status != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, status)
			}
			if reads < chunks {
				t.Errorf("expected at least %d incremental reads, got %d", chunks, reads)
			}
			if !strings.Contains(body, "data: chunk-2") {
				t.Errorf("body missing last chunk: %q", body)
			}
		})
	}
}

// TestStreaming_LargeBodyNotTruncated guards against the 2000-byte log buffer limiting what
// the client receives: an error response larger than the buffer must reach the client whole
// once it is streamed.
func TestStreaming_LargeBodyNotTruncated(t *testing.T) {
	const size = 5000
	m := New(Cfg{JsonErrors: true})
	srv := httptest.NewServer(m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			t.Errorf("flush: %v", err)
			return
		}
		if _, err := w.Write([]byte(strings.Repeat("A", size))); err != nil {
			t.Errorf("write: %v", err)
		}
	})))
	defer srv.Close()

	status, _, body := readStreamed(t, srv.URL)

	if status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", status)
	}
	if len(body) != size {
		t.Errorf("expected full %d byte body, got %d bytes", size, len(body))
	}
}

// TestStreaming_NestedStatWriters verifies that two stacked StatWriters (Logging wrapping
// JSONErrors) compose: the outer writer's releaseInterception writes into the inner one,
// which must not duplicate, drop, or envelope-wrap the streamed error body.
func TestStreaming_NestedStatWriters(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	const chunks = 3
	chain := Logging(logger)(JSONErrors(false)(streamHandler(t, http.StatusServiceUnavailable, chunks, false)))
	srv := httptest.NewServer(chain)
	defer srv.Close()

	status, reads, body := readStreamed(t, srv.URL)

	if status != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", status)
	}
	if reads < chunks {
		t.Errorf("expected at least %d incremental reads, got %d", chunks, reads)
	}
	want := "data: chunk-0\n\ndata: chunk-1\n\ndata: chunk-2\n\n"
	if body != want {
		t.Errorf("body corrupted through nested writers: got %q, want %q", body, want)
	}
}

// TestStatWriter_FlushAfterHijack verifies that flushing a hijacked writer reports
// ErrHijacked (matching net/http's Write behaviour) instead of silently succeeding.
func TestStatWriter_FlushAfterHijack(t *testing.T) {
	hw := &hijackableRW{countingRW: countingRW{}}
	sw := NewWriter(hw, true, true)

	if _, _, err := sw.Hijack(); err != nil {
		t.Fatalf("unexpected hijack error: %v", err)
	}
	if err := sw.FlushError(); !errors.Is(err, http.ErrHijacked) {
		t.Errorf("expected ErrHijacked, got %v", err)
	}
}

// readerFromRW is a countingRW that also implements io.ReaderFrom and records
// whether the fast path was used.
type readerFromRW struct {
	countingRW
	readFromCalled bool
	got            bytes.Buffer
}

func (rf *readerFromRW) ReadFrom(src io.Reader) (int64, error) {
	rf.readFromCalled = true
	if !rf.wroteHeader {
		rf.WriteHeader(http.StatusOK)
	}
	return rf.got.ReadFrom(src)
}

// TestStatWriter_ReadFrom_FastPathOnSuccess verifies that io.Copy into a StatWriter over a
// ReaderFrom-capable writer (the real net/http case, e.g. http.ServeContent) delegates to
// the underlying fast path and does not issue a superfluous WriteHeader afterwards.
func TestStatWriter_ReadFrom_FastPathOnSuccess(t *testing.T) {
	rf := &readerFromRW{}
	sw := NewWriter(rf, true, true)

	// Hide WriterTo from io.Copy (strings.Reader implements it), so the copy
	// exercises dst.ReadFrom — as it does with a real *os.File source.
	src := io.LimitReader(strings.NewReader("payload"), 7)
	n, err := io.Copy(sw, src)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 7 {
		t.Errorf("expected 7 bytes copied, got %d", n)
	}
	if !rf.readFromCalled {
		t.Error("expected the underlying ReadFrom fast path to be used")
	}
	if rf.got.String() != "payload" {
		t.Errorf("expected 'payload', got %q", rf.got.String())
	}
	sw.flushHeader()
	if rf.writeHeaderCalls != 1 {
		t.Errorf("expected exactly 1 WriteHeader call, got %d", rf.writeHeaderCalls)
	}
}

// TestStatWriter_ReadFrom_InterceptsOnError verifies that the fast path is NOT taken while
// an error body is intercepted: the bytes must go through Write so they are buffered for
// logging and withheld from the client (no-tee), exactly as a plain Write would be.
func TestStatWriter_ReadFrom_InterceptsOnError(t *testing.T) {
	rf := &readerFromRW{}
	sw := NewWriter(rf, true, false)
	sw.WriteHeader(http.StatusBadGateway)

	src := io.LimitReader(strings.NewReader("upstream error"), 14)
	if _, err := io.Copy(sw, src); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if rf.readFromCalled {
		t.Error("fast path must not bypass error-body interception")
	}
	if rf.got.Len() != 0 || rf.writeHeaderCalls != 0 {
		t.Error("intercepted body must not reach the underlying writer")
	}
	if sw.buf.String() != "upstream error" {
		t.Errorf("expected buffered body for logging, got %q", sw.buf.String())
	}
}

// TestBodyReplacement_CorrectsContentLength is the regression test for the stale
// Content-Length bug: when the middleware replaces an error body, a Content-Length set by
// the handler (or copied from an upstream by a reverse proxy) described the ORIGINAL body.
// Clients then failed the read with "unexpected EOF" and received nothing.
func TestBodyReplacement_CorrectsContentLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// http.Error sets Content-Length for the original error page
		http.Error(w, strings.Repeat("upstream detail ", 20), http.StatusBadGateway)
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	directHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Header().Set("Content-Encoding", "gzip") // stale after replacement too
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(make([]byte, 1000))
	})

	tests := []struct {
		name     string
		cfg      Cfg
		handler  http.Handler
		wantType string
	}{
		{"jsonErrors/direct", Cfg{JsonErrors: true}, directHandler, "application/json"},
		{"genericErrs/direct", Cfg{GenericErrs: true}, directHandler, "text/plain"},
		{"jsonErrors/reverseProxy", Cfg{JsonErrors: true}, httputil.NewSingleHostReverseProxy(u), "application/json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.cfg)
			srv := httptest.NewServer(m.Middleware(tc.handler))
			defer srv.Close()

			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("client failed to read replaced body: %v", err)
			}
			if len(body) == 0 {
				t.Fatal("client received an empty body")
			}
			if resp.StatusCode != http.StatusBadGateway {
				t.Errorf("expected 502, got %d", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, tc.wantType) {
				t.Errorf("expected Content-Type %s, got %q", tc.wantType, got)
			}
			if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
				t.Errorf("Content-Length %q does not match body length %d", got, len(body))
			}
			if resp.Header.Get("Content-Encoding") != "" {
				t.Errorf("stale Content-Encoding must be removed, got %q", resp.Header.Get("Content-Encoding"))
			}
		})
	}
}

// TestPanicRecover_ErrAbortHandlerPropagates is the regression test for swallowed aborts:
// http.ErrAbortHandler is net/http's sentinel to cut the connection so the client detects
// a truncated response (ReverseProxy panics with it when the upstream dies mid-copy).
// Recovering it and finishing the response normally made the truncation invisible.
func TestPanicRecover_ErrAbortHandlerPropagates(t *testing.T) {
	abortMidStream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial-data-"))
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	})

	logBuf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	for _, tc := range []struct {
		name string
		wrap func(http.Handler) http.Handler
	}{
		{"combined", func(h http.Handler) http.Handler {
			return New(Cfg{PanicRecover: true, Logger: logger}).Middleware(h)
		}},
		{"standalone", func(h http.Handler) http.Handler {
			return PanicRecover(logger)(h)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.wrap(abortMidStream))
			defer srv.Close()

			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, readErr := io.ReadAll(resp.Body)

			if string(body) != "partial-data-" {
				t.Errorf("streamed bytes must be intact and unpolluted, got %q", body)
			}
			// The whole point: the client must be able to DETECT the truncation.
			if readErr == nil {
				t.Error("expected a read error signalling truncation, got clean EOF")
			}
			if strings.Contains(logBuf.String(), "panic recovered") {
				t.Error("ErrAbortHandler must not be logged as a recovered panic")
			}
		})
	}
}

// TestPanicRecover_RealPanicStillHandled guards the other side: ordinary panics must keep
// being recovered, logged, and turned into a 500.
func TestPanicRecover_RealPanicStillHandled(t *testing.T) {
	logBuf := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logBuf, nil))
	m := New(Cfg{PanicRecover: true, Logger: logger})
	srv := httptest.NewServer(m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
	if !strings.Contains(logBuf.String(), "panic recovered") {
		t.Error("expected the panic to be logged")
	}
}

// TestStatWriter_EarlyHintsPassthrough is the regression test for 1xx handling: a handler
// sending 103 Early Hints before the real status must not have the 103 latched as the
// final status (which made the real WriteHeader a no-op and reported 200 to the client).
func TestStatWriter_EarlyHintsPassthrough(t *testing.T) {
	m := New(Cfg{JsonErrors: true})
	srv := httptest.NewServer(m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Link", "</style.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusEarlyHints) // 1xx may be sent multiple times
		w.WriteHeader(http.StatusNoContent)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected final status 204, got %d", resp.StatusCode)
	}
}

// TestHijack_EndToEnd verifies a websocket-style upgrade through the middleware: the
// handler obtains the connection, writes the raw response, and the middleware adds nothing.
func TestHijack_EndToEnd(t *testing.T) {
	m := New(Cfg{JsonErrors: true})
	srv := httptest.NewServer(m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close hijacked conn: %v", err)
			}
		}()
		if _, err := brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: raw\r\n\r\n"); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		if err := brw.Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
	})))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		// The server closes its side first, so the client close may already have failed.
		_ = conn.Close()
	}()

	if _, err = fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	if !strings.HasPrefix(got, "HTTP/1.1 101 Switching Protocols\r\n") {
		t.Errorf("expected the handler's raw 101 response, got %q", got)
	}
	if strings.Contains(got, `"error"`) {
		t.Errorf("middleware must not append a body to a hijacked response: %q", got)
	}
}
