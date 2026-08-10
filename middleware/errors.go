package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// JSONErrors returns a standalone middleware that intercepts error responses (>= 400)
// and wraps the body in a JSON envelope: {"error": "...", "code": N}.
func JSONErrors(genericErrs bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respWriter := NewWriter(w, true, false)

			next.ServeHTTP(respWriter, r)

			if respWriter.canReplaceBody() {
				errMsg := readErrMsg(respWriter)
				if genericErrs {
					errMsg = http.StatusText(respWriter.StatusCode())
				}
				b := jsonErrBytes(errMsg, respWriter.StatusCode())
				writeReplacementBody(respWriter, "application/json", b)
			} else {
				respWriter.flushHeader()
			}
		})
	}
}

// GenericErrors returns a standalone middleware that intercepts error responses (>= 400)
// and replaces the body with a generic status text (e.g., "Internal Server Error").
func GenericErrors() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respWriter := NewWriter(w, true, false)

			next.ServeHTTP(respWriter, r)

			if respWriter.canReplaceBody() {
				errMsg := http.StatusText(respWriter.StatusCode())
				writeReplacementBody(respWriter, "text/plain", []byte(errMsg))
			} else {
				respWriter.flushHeader()
			}
		})
	}
}

// writeReplacementBody writes body as the response, replacing whatever the handler
// produced. Headers describing the original body would now be wrong and are corrected:
// a stale Content-Length makes clients fail the read with "unexpected EOF" (the exact
// reverse-proxy case: upstream error pages carry a Content-Length), and a stale
// Content-Encoding would make clients try to decode a body that is no longer encoded.
func writeReplacementBody(respWriter *StatWriter, contentType string, body []byte) {
	h := respWriter.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Del("Content-Encoding")
	respWriter.flushHeader()
	_, _ = respWriter.ResponseWriter.Write(body)
	_ = flushIgnoreErr(respWriter.ResponseWriter)
}

// flushIgnoreErr flushes w if it (or anything it unwraps to) supports flushing.
func flushIgnoreErr(w http.ResponseWriter) error {
	return http.NewResponseController(w).Flush()
}

func readErrMsg(respWriter *StatWriter) string {
	msg := respWriter.buf.String()
	if respWriter.buf.Truncated() {
		msg += " [truncated]"
	}
	return msg
}

type jsonErr struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

func jsonErrBytes(errMsg string, code int) []byte {
	if code == 0 {
		code = http.StatusInternalServerError
	}
	payload := jsonErr{
		Error: errMsg,
		Code:  code,
	}
	byteErr, err := json.Marshal(payload)
	if err != nil {
		return []byte(fmt.Sprintf(`{"error":"internal error","code":%d}`, code))
	}
	return byteErr
}
