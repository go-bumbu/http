package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// JSONErrors returns a standalone middleware that intercepts error responses (>= 400)
// and wraps the body in a JSON envelope: {"error": "...", "code": N}.
func JSONErrors(genericErrs bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respWriter := NewWriter(w, true, false)

			next.ServeHTTP(respWriter, r)

			if IsStatusError(respWriter.statusCode) && !respWriter.BodyForwarded() {
				errMsg := readErrMsg(respWriter)
				if genericErrs {
					errMsg = http.StatusText(respWriter.StatusCode())
				}
				b := jsonErrBytes(errMsg, respWriter.StatusCode())
				w.Header().Set("Content-Type", "application/json")
				respWriter.flushHeader()
				_, _ = w.Write(b)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
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

			if IsStatusError(respWriter.statusCode) && !respWriter.BodyForwarded() {
				errMsg := http.StatusText(respWriter.StatusCode())
				w.Header().Set("Content-Type", "text/plain")
				respWriter.flushHeader()
				_, _ = fmt.Fprint(w, errMsg)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			} else {
				respWriter.flushHeader()
			}
		})
	}
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
