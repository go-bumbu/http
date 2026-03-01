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

// NewWriter returns a StatWriter. When interceptBody is true and status != 200,
// the body is buffered. If teeOnErr is also true, the body is also forwarded to
// the client immediately (avoids hang when e.g. a reverse proxy copies the response).
// When teeOnErr is false, only the buffer is written; the middleware must write
// the body (e.g. when it will replace it with jsonErrors or genericErrs).
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
// For non-200 when interceptBody is true: always buffers for logging.
// When teeOnErr is true, also forwards to client (so proxy copy completes; avoids hang).
// When teeOnErr is false, buffers only (middleware will write, possibly modified).
func (r *StatWriter) Write(b []byte) (int, error) {
	if r.interceptBody && r.statusCode != 200 {
		if n, err := r.buf.Write(b); err != nil {
			return n, err
		}
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

// WriteHeader writes the response status code and stores it internally
func (r *StatWriter) WriteHeader(code int) {
	if !r.headerWritten {
		r.ResponseWriter.WriteHeader(code)
		r.statusCode = code
		r.headerWritten = true
	}
}

func IsStatusError(statusCode int) bool {
	return statusCode < 200 || statusCode >= 400
}
func IsServerErr(statusCode int) bool {
	return statusCode < 200 || statusCode >= 500
}
