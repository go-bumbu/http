package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-bumbu/http/middleware"
)

type spyObserver struct {
	calls  int
	status int
	dur    time.Duration
}

func (s *spyObserver) Observe(status int, _ *http.Request, d time.Duration) {
	s.calls++
	s.status = status
	s.dur = d
}

// TestCombinedMiddleware_CallsObserver proves the combined middleware records
// metrics through the Observer seam — no Prometheus involved.
func TestCombinedMiddleware_CallsObserver(t *testing.T) {
	spy := &spyObserver{}
	m := middleware.New(middleware.Cfg{Metrics: spy})
	h := m.Wrap(testHandler(503, "boom"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	if spy.calls != 1 {
		t.Fatalf("Observe called %d times, want 1", spy.calls)
	}
	if spy.status != 503 {
		t.Errorf("observed status = %d, want 503", spy.status)
	}
	if spy.dur < 0 {
		t.Errorf("observed duration = %v, want >= 0", spy.dur)
	}
}
