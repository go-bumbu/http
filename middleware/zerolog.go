package middleware

import (
	"github.com/rs/zerolog"
	"net/http"
	"time"
)

// ZerologMiddleware logs every request using the provided logger
func ZerologMiddleware(next http.Handler, l *zerolog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeStart := time.Now()
		respWriter := NewWriter(w, false)
		// serve the request
		next.ServeHTTP(respWriter, r)
		// get the duration
		timeDiff := time.Since(timeStart)
		log(l, r, respWriter.StatusCode(), timeDiff)
	})
}

func log(l *zerolog.Logger, r *http.Request, statusCode int, dur time.Duration) {
	if IsStatusError(statusCode) {
		l.Error().
			Str("method", r.Method).
			Str("url", r.RequestURI).
			Dur("durations", dur).
			Int("response-code", statusCode).
			//Str("user-agent", r.UserAgent()).
			//Str("referer", r.Referer()).
			Str("ip", userIp(r)).
			Str("req-id", r.Header.Get("Request-Id")).
			Msg("")
	} else {
		l.Info().
			Str("method", r.Method).
			Str("url", r.RequestURI).
			Dur("durations", dur).
			Int("response-code", statusCode).
			//Str("user-agent", r.UserAgent()).
			//Str("referer", r.Referer()).
			Str("ip", userIp(r)).
			Str("req-id", r.Header.Get("Request-Id")).
			Msg("")
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
