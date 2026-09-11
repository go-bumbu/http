package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-bumbu/http/middleware"
	"github.com/go-bumbu/http/middleware/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func testHandler(statusCode int, message string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(statusCode)
		_, _ = io.WriteString(w, message)
	})
}

func scrape(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

// TestMiddleware exercises the standalone metrics middleware, NewPromHistogram,
// the label set, and the metric prefix.
func TestMiddleware(t *testing.T) {
	tcs := []struct {
		name          string
		requests      func(h http.Handler)
		metricPrefix  string
		expectedLines []string
	}{
		{
			name: "simple test",
			requests: func(h http.Handler) {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/bla", nil))
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/ble/bli", nil))
			},
			expectedLines: []string{
				`requests_http_duration_seconds_bucket{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1",le="0.005"} 1`,
				`requests_http_duration_seconds_bucket{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1",le="0.01"} 1`,
				`requests_http_duration_seconds_bucket{addr="/ble/bli",isError="false",method="POST",status="200",type="HTTP/1.1",le="0.01"} 1`,
				`requests_http_duration_seconds_bucket{addr="/ble/bli",isError="false",method="POST",status="200",type="HTTP/1.1",le="0.25"} 1`,
			},
		},
		{
			name: "metric prefix",
			requests: func(h http.Handler) {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/bla", nil))
			},
			metricPrefix: "ehmm",
			expectedLines: []string{
				`ehmm_http_duration_seconds_bucket{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1",le="0.005"} 1`,
				`ehmm_http_duration_seconds_bucket{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1",le="0.01"} 1`,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			hist, err := metrics.NewPromHistogram(tc.metricPrefix, nil, reg)
			if err != nil {
				t.Fatalf("failed to create histogram: %v", err)
			}
			h := metrics.Middleware(hist)(testHandler(200, "ok"))
			tc.requests(h)

			body := scrape(t, reg)
			for _, line := range tc.expectedLines {
				if !strings.Contains(body, line) {
					t.Errorf("response does not contain expected line: %s", line)
				}
			}
		})
	}
}

// TestMiddleware_PatternLabel verifies that when the handler is routed through a
// pattern-aware http.ServeMux, the "addr" label uses the route pattern instead of
// the raw path — one time series per route, not one per distinct URL.
func TestMiddleware_PatternLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	hist, err := metrics.NewPromHistogram("", nil, reg)
	if err != nil {
		t.Fatalf("failed to create histogram: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", testHandler(200, "ok"))
	h := metrics.Middleware(hist)(mux)
	for _, id := range []string{"1", "2", "3"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/"+id, nil))
	}

	body := scrape(t, reg)
	want := `requests_http_duration_seconds_count{addr="GET /users/{id}",isError="false",method="GET",status="200",type="HTTP/1.1"} 3`
	if !strings.Contains(body, want) {
		t.Errorf("expected a single pattern-labelled series:\n%s\nmetrics:\n%s", want, body)
	}
	if strings.Contains(body, `addr="/users/1"`) {
		t.Error("raw parametrised path must not be used as label when a pattern is available")
	}
}

// TestCombinedMiddleware_Metrics proves the combined middleware wires a real
// Prometheus observer end to end via Cfg.Metrics.
func TestCombinedMiddleware_Metrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	hist, err := metrics.NewPromHistogram("", nil, reg)
	if err != nil {
		t.Fatalf("failed to create histogram: %v", err)
	}
	m := middleware.New(middleware.Cfg{Metrics: metrics.NewObserver(hist)})
	h := m.Middleware(testHandler(200, "ok"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/bla", nil))

	body := scrape(t, reg)
	want := `requests_http_duration_seconds_count{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("combined middleware did not record metrics via the Observer seam:\n%s", body)
	}
}

// TestNilHistogram_Passthrough verifies the zero Histogram disables metrics
// without panicking and leaves the response untouched.
func TestNilHistogram_Passthrough(t *testing.T) {
	if obs := metrics.NewObserver(metrics.Histogram{}); obs != nil {
		t.Errorf("NewObserver(zero) = %v, want nil", obs)
	}
	rec := httptest.NewRecorder()
	metrics.Middleware(metrics.Histogram{})(testHandler(204, "")).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 204 {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
