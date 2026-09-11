package middleware

import (
	"context"
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

// Logger is the subset of *slog.Logger the middleware depends on for request
// logging: just Enabled and LogAttrs. *slog.Logger satisfies it, so callers keep
// passing slog.New(handler) or slog.Default() unchanged; the interface lets custom
// or test loggers be substituted and keeps the middleware package from
// hard-depending on a concrete logger type.
type Logger interface {
	Enabled(ctx context.Context, level slog.Level) bool
	LogAttrs(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr)
}

// defaultRequestIDHeader is the request header the built-in correlation-id
// extractor reads when Cfg.RequestID is nil.
const defaultRequestIDHeader = "Request-Id"

// defaultRequestID is the built-in Cfg.RequestID: it reads the correlation id
// from the defaultRequestIDHeader request header.
func defaultRequestID(r *http.Request) string {
	return r.Header.Get(defaultRequestIDHeader)
}

func (m *Middleware) log(r *http.Request, statusCode int, errmsg string, dur time.Duration) {
	if m.logger == nil {
		return
	}

	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("url", r.RequestURI),
		slog.Duration("req-dur", dur),
		slog.Int("response-code", statusCode),
		slog.String("ip", userIp(r)),
		slog.String("req-id", m.requestID(r)),
	}
	if IsStatusError(statusCode) {
		attrs = append(attrs, slog.String("err-msg", errmsg))
	}

	level := slog.LevelInfo
	if IsServerErr(statusCode) {
		level = slog.LevelError
	}

	m.logger.LogAttrs(r.Context(), level, "", attrs...)
}

// logHeadersDebug emits a single slog.LevelDebug record containing request and
// response headers, with redaction applied according to the middleware config.
// No-op when header logging is off, no logger is configured, or the logger is
// not enabled for LevelDebug (avoids iterating header maps in that case).
func (m *Middleware) logHeadersDebug(r *http.Request, respHeaders http.Header) {
	if !m.logHeaders || m.logger == nil {
		return
	}
	if !m.logger.Enabled(r.Context(), slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("url", r.RequestURI),
		slog.String("req-id", m.requestID(r)),
		m.redact.headerAttrs("req-headers", r.Header, m.disableRedaction),
		m.redact.headerAttrs("resp-headers", respHeaders, m.disableRedaction),
	}
	m.logger.LogAttrs(r.Context(), slog.LevelDebug, "", attrs...)
}

// userIp returns the client IP for logging. X-Real-Ip and X-Forwarded-For are trusted
// unconditionally: this assumes the server runs behind a reverse proxy that sets them.
// If the server is exposed directly, clients can spoof these headers — do not use the
// logged IP for security decisions.
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
