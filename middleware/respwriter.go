package middleware

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
)

// StatWriter is a wrapper to a httpResponse writer that allows to intercept and
// extract the status code that the upstream code has defined
type StatWriter struct {
	http.ResponseWriter
	statusCode    int
	buf           *limitBuf
	headerWritten bool
	streaming     bool // true once the handler flushed: body interception is released
	hijacked      bool // true once the connection was taken over by the handler
}

// bufMaxBytes caps how many bytes of an error response body are retained for
// logging. It bounds per-request memory and log-line width; content past the cap
// is dropped and flagged via limitBuf.Truncated.
const bufMaxBytes = 2000

// NewWriter returns a StatWriter wrapping w. It records the response status code and, on an
// error status (>= 400, see IsStatusError), buffers the response body for logging (capped at
// bufMaxBytes) while simultaneously forwarding it to the client — the tee avoids a hang when
// e.g. a reverse proxy copies the response. Success and 1xx responses pass straight through.
func NewWriter(w http.ResponseWriter) *StatWriter {
	return &StatWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		buf:            newLimitBuf(bufMaxBytes),
	}
}

func (r *StatWriter) StatusCode() int {
	return r.statusCode
}

// Write buffers error-response bodies for logging (bounded by bufMaxBytes) while always
// forwarding them to the underlying writer, so the client — or a reverse proxy copying the
// response — receives the body and does not hang. Success responses pass straight through.
func (r *StatWriter) Write(b []byte) (int, error) {
	if IsStatusError(r.statusCode) {
		// Buffer for logging; excess bytes are silently dropped (observable via limitBuf.Truncated).
		_, _ = r.buf.Write(b)
	}
	// The underlying Write implicitly commits the header (WriteHeader(200) if not
	// already written); record it so flushHeader does not write the header twice.
	r.headerWritten = true
	return r.ResponseWriter.Write(b)
}

// ReadFrom implements io.ReaderFrom so that io.Copy-based handlers (http.ServeContent,
// http.FileServer, ReverseProxy without a BufferPool) keep the underlying writer's
// sendfile fast path. It only delegates on the success passthrough path; error responses go
// through Write, which buffers them for logging and tees them to the client.
func (r *StatWriter) ReadFrom(src io.Reader) (int64, error) {
	rf, ok := r.ResponseWriter.(io.ReaderFrom)
	if !ok || IsStatusError(r.statusCode) {
		return io.Copy(writerOnly{r}, src)
	}
	// The underlying ReadFrom implicitly commits the header, like Write does.
	r.headerWritten = true
	return rf.ReadFrom(src)
}

// writerOnly hides ReadFrom from io.Copy so the copy goes through StatWriter.Write
// instead of recursing back into StatWriter.ReadFrom.
type writerOnly struct {
	io.Writer
}

// WriteHeader records and forwards the response status code. 1xx informational responses
// (e.g. 103 Early Hints) pass through without latching, so the real final status is still
// captured and written.
func (r *StatWriter) WriteHeader(code int) {
	if r.headerWritten || r.hijacked {
		return
	}
	if code >= 100 && code < 200 {
		// 1xx informational responses (e.g. 103 Early Hints) may be sent multiple
		// times before the final status; pass through without latching, so the
		// real status code is still captured and written later.
		r.ResponseWriter.WriteHeader(code)
		return
	}
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
	r.headerWritten = true
}

// flushHeader ensures the status code is written to the underlying ResponseWriter.
// Called by the middleware after it has set final headers.
func (r *StatWriter) flushHeader() {
	if r.hijacked {
		// The handler owns the connection; writing a header would corrupt the raw
		// response (net/http logs "WriteHeader on hijacked connection").
		return
	}
	if !r.headerWritten {
		r.ResponseWriter.WriteHeader(r.statusCode)
		r.headerWritten = true
	}
}

// Flush implements http.Flusher so that handlers and nested middleware using the
// pre-Go1.20 `w.(http.Flusher)` type assertion can still stream through this wrapper.
// Errors are discarded, matching the http.Flusher contract; use FlushError to observe them.
func (r *StatWriter) Flush() {
	_ = r.FlushError()
}

// FlushError implements the interface http.ResponseController.Flush prefers. It releases
// body interception (see releaseInterception) before delegating, so that a flush cannot
// implicitly commit a 200 header and discard the real status code.
func (r *StatWriter) FlushError() error {
	if r.hijacked {
		return http.ErrHijacked
	}
	if !supportsFlush(r.ResponseWriter) {
		// Nothing can be streamed; leave interception intact so the error body is
		// still captured for logging.
		return errFlushNotSupported()
	}
	r.releaseInterception()
	return http.NewResponseController(r.ResponseWriter).Flush()
}

// Hijack implements http.Hijacker, both for handlers using the type assertion directly
// and to record that the connection was taken over: after a hijack the middleware must
// not write a status code or body to the underlying writer.
func (r *StatWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	// The handler now writes the raw response itself. hijacked suppresses our header
	// write; streaming stops the middleware from synthesising a body afterwards.
	r.hijacked = true
	r.streaming = true
	return conn, brw, nil
}

// releaseInterception switches the writer to passthrough mode. It is called on the first
// flush: a handler that flushes is streaming, so the status code must be committed before
// the flush implicitly writes 200. Error bodies are already teed to the client as they are
// written, so nothing is buffered-but-unsent to forward here.
func (r *StatWriter) releaseInterception() {
	if r.streaming {
		return
	}
	r.streaming = true
	r.flushHeader()
}

// Streaming reports whether the handler flushed or hijacked the response, meaning the body
// has already reached the client and the middleware must not synthesise one over it.
func (r *StatWriter) Streaming() bool {
	return r.streaming
}

// Started reports whether the response has been committed: a status code was written to the
// underlying writer, explicitly via WriteHeader or implicitly by the first Write/ReadFrom.
// Once started, the middleware must not synthesise a body (e.g. a 500 after a recovered
// panic): it would append to the partial response under the already-sent status, which
// net/http itself declines to do.
func (r *StatWriter) Started() bool {
	return r.headerWritten
}

// Unwrap returns the underlying ResponseWriter, allowing http.ResponseController
// to access optional interfaces (Hijacker, deadline setters) on the original writer.
func (r *StatWriter) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// supportsFlush reports whether w, or any writer it unwraps to, can flush. It mirrors the
// lookup http.ResponseController.Flush performs, so FlushError can decide whether to
// release interception before a flush that would otherwise be a no-op.
func supportsFlush(w http.ResponseWriter) bool {
	for {
		switch t := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = t.Unwrap()
		default:
			return false
		}
	}
}

// errFlushNotSupported returns an error matching http.ErrNotSupported, as
// http.ResponseController does for writers that cannot flush.
func errFlushNotSupported() error {
	return fmt.Errorf("%w", http.ErrNotSupported)
}

func IsStatusError(statusCode int) bool {
	return statusCode >= 400
}

func IsServerErr(statusCode int) bool {
	return statusCode >= 500
}
