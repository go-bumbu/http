package middleware

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/go-bumbu/http/lib/limitio"
)

// StatWriter is a wrapper to a httpResponse writer that allows to intercept and
// extract the status code that the upstream code has defined
type StatWriter struct {
	http.ResponseWriter
	statusCode    int
	interceptBody bool // buffer body for non-200 responses
	teeOnErr      bool // when true, also forward body to client (avoids hang on proxy copy)
	buf           *limitio.LimitedBuf
	headerWritten bool
	bodyForwarded bool // true when body was written to client (via tee)
	streaming     bool // true once the handler flushed: body interception is released
	hijacked      bool // true once the connection was taken over by the handler
}

// NewWriter returns a StatWriter. When interceptBody is true and status is an error
// (>= 400, see IsStatusError), the body is buffered. If teeOnErr is also true, the body is also
// forwarded to the client immediately (avoids hang when e.g. a reverse proxy copies the
// response). When teeOnErr is false, only the buffer is written; the middleware must
// write the body (e.g. when it will replace it with jsonErrors or genericErrs).
func NewWriter(w http.ResponseWriter, interceptBody bool, teeOnErr bool) *StatWriter {
	return &StatWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		interceptBody:  interceptBody,
		teeOnErr:       teeOnErr,
		buf: &limitio.LimitedBuf{
			Buffer:   bytes.Buffer{},
			MaxBytes: 2000,
		},
	}
}

func (r *StatWriter) StatusCode() int {
	return r.statusCode
}

func (r *StatWriter) StatusCodeStr() string {
	return strconv.Itoa(r.statusCode)
}

// Write returns underlying Write result.
// For error responses when interceptBody is true: always buffers for logging.
// When teeOnErr is true, also forwards to client (so proxy copy completes; avoids hang).
// When teeOnErr is false, buffers only (middleware will write, possibly modified) — unless
// the handler has flushed, which releases interception so the response can stream.
func (r *StatWriter) Write(b []byte) (int, error) {
	if r.interceptBody && IsStatusError(r.statusCode) {
		// Buffer for logging; ignore ErrBufferLimit since partial content is acceptable for logging
		_, _ = r.buf.Write(b)
		if r.teeOnErr || r.streaming {
			n, err := r.ResponseWriter.Write(b)
			if n > 0 {
				r.bodyForwarded = true
			}
			// The underlying Write implicitly committed the header; record it so
			// flushHeader does not issue a superfluous WriteHeader call.
			r.headerWritten = true
			return n, err
		}
		return len(b), nil
	}
	// The underlying Write implicitly commits the header (WriteHeader(200) if not
	// already written); record it so flushHeader does not write the header twice.
	r.headerWritten = true
	return r.ResponseWriter.Write(b)
}

// BodyForwarded returns true if the response body was already written to the client
// (e.g. via tee during a proxy copy). The middleware uses this to avoid writing twice.
func (r *StatWriter) BodyForwarded() bool {
	return r.bodyForwarded
}

// ReadFrom implements io.ReaderFrom so that io.Copy-based handlers (http.ServeContent,
// http.FileServer, ReverseProxy without a BufferPool) keep the underlying writer's
// sendfile fast path. It only delegates on the plain passthrough path; error responses
// under interception go through Write, which buffers (and tees) as configured.
func (r *StatWriter) ReadFrom(src io.Reader) (int64, error) {
	rf, ok := r.ResponseWriter.(io.ReaderFrom)
	if !ok || (r.interceptBody && IsStatusError(r.statusCode)) {
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

// WriteHeader stores the response status code. When body interception is active and the
// body will be replaced (teeOnErr is false), the actual header write is deferred so the
// middleware can set correct Content-Type/Content-Length before flushing.
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
	if r.interceptBody && !r.teeOnErr && IsStatusError(code) && !r.streaming {
		// Defer: middleware will write headers after determining the final body.
		return
	}
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
		// Nothing can be streamed; keep interception intact so the middleware can
		// still replace the body as configured.
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
	// write; streaming stops the middleware from appending a replacement body.
	r.hijacked = true
	r.streaming = true
	return conn, brw, nil
}

// releaseInterception switches the writer to passthrough mode. It is called on the first
// flush: a handler that flushes is streaming, so the body can no longer be buffered and
// replaced. Anything already buffered is forwarded to the client first, after committing
// the deferred status code.
//
// NOTE: content buffered beyond the buffer limit before the first flush is lost, since the
// buffer is sized for logging. A handler writing more than limitio bufMaxBytes before its
// first flush is not really streaming, so this is accepted.
func (r *StatWriter) releaseInterception() {
	if r.streaming {
		return
	}
	r.streaming = true

	if !r.interceptBody || r.teeOnErr || !IsStatusError(r.statusCode) {
		// Body was never held back; only the header may still be pending.
		r.flushHeader()
		return
	}

	// Commit the real status code before the flush implicitly commits 200.
	r.flushHeader()
	// Forward what was buffered so the stream is not missing its leading bytes.
	// Bytes() does not consume, so the buffer stays available for logging.
	if b := r.buf.Bytes(); len(b) > 0 {
		if n, err := r.ResponseWriter.Write(b); n > 0 && err == nil {
			r.bodyForwarded = true
		}
	}
	// The response is now a stream: the middleware must not append a replacement body.
	r.bodyForwarded = true
}

// Streaming reports whether the handler flushed the response, meaning the body was
// streamed to the client and cannot be intercepted or replaced.
func (r *StatWriter) Streaming() bool {
	return r.streaming
}

// canReplaceBody reports whether the middleware may still write the response body itself.
// False when the status is not an error, when the body already reached the client (tee or
// stream), or when the handler hijacked the connection.
func (r *StatWriter) canReplaceBody() bool {
	return IsStatusError(r.statusCode) && !r.bodyForwarded && !r.streaming && !r.hijacked
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
