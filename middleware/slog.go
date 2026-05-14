package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// defaultRedactHeaders is the built-in list of header names whose values are
// replaced with "[REDACTED]" when LogHeaders is enabled. Kept unexported —
// callers extend it via Cfg.ExtraRedactHeaders.
var defaultRedactHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"Set-Cookie",
	"X-Api-Key",
	"X-Auth-Token",
	"X-Csrf-Token",
}

// redactSet is the canonicalised set of header keys whose values must be
// replaced with "[REDACTED]" in the debug header log.
type redactSet map[string]struct{}

// newRedactSet returns the union of defaultRedactHeaders and extra, with all
// keys canonicalised via http.CanonicalHeaderKey.
func newRedactSet(extra []string) redactSet {
	s := make(redactSet, len(defaultRedactHeaders)+len(extra))
	for _, k := range defaultRedactHeaders {
		s[http.CanonicalHeaderKey(k)] = struct{}{}
	}
	for _, k := range extra {
		s[http.CanonicalHeaderKey(k)] = struct{}{}
	}
	return s
}

// headerAttrs returns a slog.Group attribute built from h. When disabled is
// false, any key present in s has its value replaced with "[REDACTED]";
// otherwise all values are rendered verbatim. Multi-value headers are joined
// with ", " (matching http.Header.Values display semantics).
func (s redactSet) headerAttrs(groupName string, h http.Header, disabled bool) slog.Attr {
	attrs := make([]any, 0, len(h))
	for k, vs := range h {
		canon := http.CanonicalHeaderKey(k)
		var val string
		if !disabled {
			if _, redact := s[canon]; redact {
				val = "[REDACTED]"
				attrs = append(attrs, slog.String(canon, val))
				continue
			}
		}
		val = strings.Join(vs, ", ")
		attrs = append(attrs, slog.String(canon, val))
	}
	return slog.Group(groupName, attrs...)
}

// Logging returns a standalone middleware that logs requests using structured logging.
// Error responses (>= 400) include the response body in the log.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	m := &Middleware{logger: logger}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timeStart := time.Now()
			respWriter := NewWriter(w, true, true)

			next.ServeHTTP(respWriter, r)
			timeDiff := time.Since(timeStart)

			errMsg := m.getErrMsg(respWriter.statusCode, respWriter.buf)
			m.log(r, respWriter.StatusCode(), errMsg, timeDiff)

			respWriter.flushHeader()
		})
	}
}

func (c *Middleware) log(r *http.Request, statusCode int, errmsg string, dur time.Duration) {
	if c.logger == nil {
		return
	}

	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("url", r.RequestURI),
		slog.Duration("req-dur", dur),
		slog.Int("response-code", statusCode),
		slog.String("ip", userIp(r)),
		slog.String("req-id", r.Header.Get("Request-Id")),
	}
	if IsStatusError(statusCode) {
		attrs = append(attrs, slog.String("err-handlerMsg", errmsg))
	}

	level := slog.LevelInfo
	if IsServerErr(statusCode) {
		level = slog.LevelError
	}

	c.logger.LogAttrs(r.Context(), level, "", attrs...)
}

// logHeadersDebug emits a single slog.LevelDebug record containing request and
// response headers, with redaction applied according to the middleware config.
// No-op when header logging is off, no logger is configured, or the logger is
// not enabled for LevelDebug (avoids iterating header maps in that case).
func (c *Middleware) logHeadersDebug(r *http.Request, respHeaders http.Header) {
	if !c.logHeaders || c.logger == nil {
		return
	}
	if !c.logger.Enabled(r.Context(), slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("url", r.RequestURI),
		slog.String("req-id", r.Header.Get("Request-Id")),
		c.redact.headerAttrs("req-headers", r.Header, c.disableRedaction),
		c.redact.headerAttrs("resp-headers", respHeaders, c.disableRedaction),
	}
	c.logger.LogAttrs(r.Context(), slog.LevelDebug, "", attrs...)
}

func userIp(r *http.Request) string {
	IPAddress := r.Header.Get("X-Real-Ip")
	if IPAddress == "" {
		IPAddress = r.Header.Get("X-Forwarded-For")
	}
	if IPAddress == "" {
		IPAddress = r.RemoteAddr
	}
	return IPAddress
}
