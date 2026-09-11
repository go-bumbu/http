package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReqDelay(t *testing.T) {
	// A generous upper bound so timing assertions don't flake on a busy CI box.
	const tolerance = 500 * time.Millisecond

	tests := []struct {
		name     string
		delay    ReqDelay
		minSleep time.Duration
		maxSleep time.Duration
	}{
		{
			name:     "disabled does not delay",
			delay:    ReqDelay{On: false, MinDelay: 50 * time.Millisecond, MaxDelay: 100 * time.Millisecond},
			minSleep: 0,
			maxSleep: tolerance,
		},
		{
			name:     "equal min and max sleeps min without panicking",
			delay:    ReqDelay{On: true, MinDelay: 20 * time.Millisecond, MaxDelay: 20 * time.Millisecond},
			minSleep: 20 * time.Millisecond,
			maxSleep: 20*time.Millisecond + tolerance,
		},
		{
			name:     "min greater than max sleeps min without panicking",
			delay:    ReqDelay{On: true, MinDelay: 30 * time.Millisecond, MaxDelay: 10 * time.Millisecond},
			minSleep: 30 * time.Millisecond,
			maxSleep: 30*time.Millisecond + tolerance,
		},
		{
			name:     "jitter stays within range",
			delay:    ReqDelay{On: true, MinDelay: 10 * time.Millisecond, MaxDelay: 40 * time.Millisecond},
			minSleep: 10 * time.Millisecond,
			maxSleep: 40*time.Millisecond + tolerance,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			start := time.Now()
			tc.delay.Delay(next).ServeHTTP(rec, req)
			elapsed := time.Since(start)

			if !called {
				t.Fatal("next handler was not called")
			}
			if elapsed < tc.minSleep {
				t.Errorf("elapsed %v < expected minimum %v", elapsed, tc.minSleep)
			}
			if elapsed > tc.maxSleep {
				t.Errorf("elapsed %v > expected maximum %v", elapsed, tc.maxSleep)
			}
		})
	}
}

func TestReqDelay_HonoursContextCancellation(t *testing.T) {
	d := ReqDelay{On: true, MinDelay: 2 * time.Second, MaxDelay: 3 * time.Second}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the delay must return immediately

	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	start := time.Now()
	d.Delay(next).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("delay ignored context cancellation: waited %v", elapsed)
	}
	if called {
		t.Error("next handler must not run when the context is cancelled during the delay")
	}
}
