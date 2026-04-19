package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

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
