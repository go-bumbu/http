package middleware

import (
	"math/rand/v2"
	"net/http"
	"time"
)

// ReqDelay is middleware that pauses each request before invoking the next
// handler. It is a development aid for simulating slow responses and stays
// disabled unless On is true.
type ReqDelay struct {
	MinDelay time.Duration
	MaxDelay time.Duration
	On       bool
}

// Delay returns a middleware that, when On is true, sleeps for a random
// duration in [MinDelay, MaxDelay) before calling next. When MaxDelay is not
// greater than MinDelay the pause is exactly MinDelay (no jitter). The sleep
// honours the request context, so a client that disconnects mid-delay is not
// made to wait it out.
func (t ReqDelay) Delay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t.On {
			d := t.MinDelay
			if size := t.MaxDelay - t.MinDelay; size > 0 {
				d += rand.N(size) //nolint:gosec // non-crypto randomness is sufficient for delay jitter
			}
			if d > 0 {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
