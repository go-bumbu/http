package middleware

import (
	"math/rand/v2"
	"net/http"
	"time"
)

type ReqDelay struct {
	MinDelay time.Duration
	MaxDelay time.Duration
	On       bool
}

func (t ReqDelay) Delay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if t.On && t.MinDelay.Milliseconds() != 0 && t.MaxDelay.Milliseconds() != 0 {
			size := t.MaxDelay - t.MinDelay
			randDur := rand.IntN(int(size))
			time.Sleep(time.Duration(randDur))
		}
		next.ServeHTTP(w, r)
	})
}
