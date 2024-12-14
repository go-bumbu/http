package middleware

import (
	"bytes"
	"github.com/go-bumbu/http/lib/limitio"
	"net/http"
	"strconv"
)

// StatWriter is a wrapper to a httpResponse writer that allows to intercept and
// extract the status code that the upstream code has defined
type StatWriter struct {
	http.ResponseWriter
	statusCode    int
	interceptBody bool // write a limited amount of chars into a buffer in case a non 200 code
	buf           *limitio.LimitedBuf
	headerWritten bool // only write header once
}

// NewWriter will return a pointer to a response writer
// if teeBodyOnErr is set to true, and in case of a non 200 status code,
// the writer will not write the Response but instead into it's own buffer.
// this allows to modify the response before writing it to the output
func NewWriter(w http.ResponseWriter, teeBodyOnErr bool) *StatWriter {
	// WriteHeader(int) is not called if the response is 200 (implicit response code) so it needs to be the default
	return &StatWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		interceptBody:  teeBodyOnErr,
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

// Write returns underlying Write result, while counting data size
func (r *StatWriter) Write(b []byte) (int, error) {
	if r.interceptBody && r.statusCode != 200 {
		return r.buf.Write(b)
	}
	return r.ResponseWriter.Write(b)
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
