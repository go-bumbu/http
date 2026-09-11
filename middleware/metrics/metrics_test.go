package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestObserver_Records drives one request through the combined middleware and
// asserts the scraped series: the metric prefix, the bounded method label, the
// "unmatched" addr for un-routed requests, and that the derived isError label is
// gone (a consumer computes status >= 400 in PromQL).
func TestObserver_Records(t *testing.T) {
	// net/http delivers any RFC token as the method verbatim; set it directly so
	// the test does not depend on httptest's own method validation.
	newReq := func(method, target string) *http.Request {
		r := httptest.NewRequest("GET", target, nil)
		r.Method = method
		return r
	}
	tcs := []struct {
		name    string
		prefix  string
		method  string
		want    string
		notWant string
	}{
		{
			name:   "default prefix, standard method, no mux",
			method: "GET",
			want:   `requests_http_duration_seconds_count{addr="unmatched",method="GET",status="200",type="HTTP/1.1"} 1`,
		},
		{
			name:   "custom prefix",
			prefix: "ehmm",
			method: "POST",
			want:   `ehmm_http_duration_seconds_count{addr="unmatched",method="POST",status="200",type="HTTP/1.1"} 1`,
		},
		{
			name:   "nonstandard method collapses to other",
			method: "ZZQ123",
			want:   `requests_http_duration_seconds_count{addr="unmatched",method="other",status="200",type="HTTP/1.1"} 1`,
		},
		{
			name:    "isError label dropped",
			method:  "GET",
			want:    `requests_http_duration_seconds_count{addr="unmatched",method="GET",status="200",type="HTTP/1.1"} 1`,
			notWant: "isError",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			obs, err := metrics.NewObserver(metrics.Cfg{Prefix: tc.prefix, Registry: reg})
			if err != nil {
				t.Fatalf("NewObserver: %v", err)
			}
			h := middleware.New(middleware.Cfg{Metrics: obs}).Wrap(testHandler(200, "ok"))
			h.ServeHTTP(httptest.NewRecorder(), newReq(tc.method, "/bla"))

			body := scrape(t, reg)
			if !strings.Contains(body, tc.want) {
				t.Errorf("missing expected series:\n%s\ngot:\n%s", tc.want, body)
			}
			if tc.notWant != "" && strings.Contains(body, tc.notWant) {
				t.Errorf("unexpected substring %q present:\n%s", tc.notWant, body)
			}
		})
	}
}

// TestObserver_DefaultBuckets verifies empty buckets fall back to
// prometheus.DefBuckets (a representative le line is emitted).
func TestObserver_DefaultBuckets(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs, err := metrics.NewObserver(metrics.Cfg{Registry: reg})
	if err != nil {
		t.Fatalf("NewObserver: %v", err)
	}
	middleware.New(middleware.Cfg{Metrics: obs}).Wrap(testHandler(200, "ok")).
		ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/bla", nil))

	body := scrape(t, reg)
	want := `requests_http_duration_seconds_bucket{addr="unmatched",method="GET",status="200",type="HTTP/1.1",le="0.005"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("missing default-bucket series:\n%s\ngot:\n%s", want, body)
	}
}

// TestObserver_PatternLabel verifies that, behind a pattern-aware http.ServeMux,
// the "addr" label is the matched route pattern — one series per route, never
// one per distinct URL (the raw parametrised path must not appear).
func TestObserver_PatternLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs, err := metrics.NewObserver(metrics.Cfg{Registry: reg})
	if err != nil {
		t.Fatalf("NewObserver: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", testHandler(200, "ok"))
	h := middleware.New(middleware.Cfg{Metrics: obs}).Wrap(mux)
	for _, id := range []string{"1", "2", "3"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/users/"+id, nil))
	}

	body := scrape(t, reg)
	want := `requests_http_duration_seconds_count{addr="GET /users/{id}",method="GET",status="200",type="HTTP/1.1"} 3`
	if !strings.Contains(body, want) {
		t.Errorf("expected a single pattern-labelled series:\n%s\nmetrics:\n%s", want, body)
	}
	if strings.Contains(body, `addr="/users/1"`) {
		t.Error("raw parametrised path must not be used as a label when a pattern is available")
	}
}

// TestNopObserver verifies NopObserver is a non-nil Observer that is safe to call
// directly and leaves the response untouched — this is the sharp edge a nil
// interface sentinel would hit.
func TestNopObserver(t *testing.T) {
	obs := metrics.NopObserver()
	if obs == nil {
		t.Fatal("NopObserver() = nil, want a non-nil Observer")
	}
	obs.Observe(200, httptest.NewRequest("GET", "/x", nil), time.Second) // must not panic

	rec := httptest.NewRecorder()
	middleware.New(middleware.Cfg{Metrics: obs}).Wrap(testHandler(204, "")).
		ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 204 {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

// TestNewObserver_DuplicateRegister covers the registry.Register error path:
// registering a second histogram with the same name on one registry must fail
// and yield a nil Observer.
func TestNewObserver_DuplicateRegister(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := metrics.NewObserver(metrics.Cfg{Registry: reg}); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	obs, err := metrics.NewObserver(metrics.Cfg{Registry: reg})
	if err == nil {
		t.Fatal("expected an error registering a duplicate histogram, got nil")
	}
	if obs != nil {
		t.Errorf("expected a nil Observer on error, got %v", obs)
	}
}

// TestNewObserver_BucketValidation checks that non-strictly-increasing buckets
// are rejected at construction, instead of panicking later from inside Observe
// (Prometheus validates bucket ordering lazily on the first observation).
func TestNewObserver_BucketValidation(t *testing.T) {
	tcs := []struct {
		name    string
		buckets []float64
		wantErr bool
	}{
		{name: "nil defaults to DefBuckets", buckets: nil},
		{name: "single bucket", buckets: []float64{0.1}},
		{name: "strictly increasing", buckets: []float64{0.1, 0.2, 0.3}},
		{name: "descending", buckets: []float64{1, 0.5}, wantErr: true},
		{name: "equal neighbours", buckets: []float64{0.1, 0.1}, wantErr: true},
		{name: "out of order in the middle", buckets: []float64{0.1, 0.3, 0.2}, wantErr: true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			obs, err := metrics.NewObserver(metrics.Cfg{Buckets: tc.buckets, Registry: prometheus.NewRegistry()})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if obs != nil {
					t.Errorf("expected a nil Observer on error, got %v", obs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if obs == nil {
				t.Fatal("expected a non-nil Observer")
			}
		})
	}
}
