package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

func (c *Middleware) log(r *http.Request, statusCode int, errmsg string, dur time.Duration) {
	if c.logger == nil {
		return
	}
	if IsServerErr(statusCode) {
		c.logger.Error("",
			slog.String("method", r.Method),
			slog.String("url", r.RequestURI),
			slog.Duration("req-dur", dur),
			slog.Int("response-code", statusCode),
			slog.String("ip", userIp(r)),
			slog.String("req-id", r.Header.Get("Request-Id")),
			slog.String("err-handlerMsg", errmsg),
		)
	} else {
		c.logger.Info("",
			slog.String("method", r.Method),
			slog.String("url", r.RequestURI),
			slog.Duration("req-dur", dur),
			slog.Int("response-code", statusCode),
			slog.String("ip", userIp(r)),
			slog.String("req-id", r.Header.Get("Request-Id")),
		)
	}
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
