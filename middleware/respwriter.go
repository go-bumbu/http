package middleware

import (
	"bytes"
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
}

// NewWriter returns a StatWriter. When interceptBody is true and status is an error
// (< 200 or >= 400), the body is buffered. If teeOnErr is also true, the body is also
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
// When teeOnErr is false, buffers only (middleware will write, possibly modified).
func (r *StatWriter) Write(b []byte) (int, error) {
	if r.interceptBody && IsStatusError(r.statusCode) {
		// Buffer for logging; ignore ErrBufferLimit since partial content is acceptable for logging
		_, _ = r.buf.Write(b)
		if r.teeOnErr {
			n, err := r.ResponseWriter.Write(b)
			if n > 0 {
				r.bodyForwarded = true
			}
			return n, err
		}
		return len(b), nil
	}
	return r.ResponseWriter.Write(b)
}

// BodyForwarded returns true if the response body was already written to the client
// (e.g. via tee during a proxy copy). The middleware uses this to avoid writing twice.
func (r *StatWriter) BodyForwarded() bool {
	return r.bodyForwarded
}

// WriteHeader stores the response status code. When body interception is active and the
// body will be replaced (teeOnErr is false), the actual header write is deferred so the
// middleware can set correct Content-Type/Content-Length before flushing.
func (r *StatWriter) WriteHeader(code int) {
	if r.headerWritten {
		return
	}
	r.statusCode = code
	if r.interceptBody && !r.teeOnErr && IsStatusError(code) {
		// Defer: middleware will write headers after determining the final body.
		return
	}
	r.ResponseWriter.WriteHeader(code)
	r.headerWritten = true
}

// flushHeader ensures the status code is written to the underlying ResponseWriter.
// Called by the middleware after it has set final headers.
func (r *StatWriter) flushHeader() {
	if !r.headerWritten {
		r.ResponseWriter.WriteHeader(r.statusCode)
		r.headerWritten = true
	}
}

// Unwrap returns the underlying ResponseWriter, allowing http.ResponseController
// to access optional interfaces (Flusher, Hijacker) on the original writer.
func (r *StatWriter) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func IsStatusError(statusCode int) bool {
	return statusCode >= 400
}

func IsServerErr(statusCode int) bool {
	return statusCode >= 500
}
