package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics returns a standalone middleware that records Prometheus request duration metrics.
func Metrics(hist Histogram) func(http.Handler) http.Handler {
	if hist.h == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	m := &Middleware{hist: hist}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timeStart := time.Now()
			respWriter := NewWriter(w, false, false)

			next.ServeHTTP(respWriter, r)
			timeDiff := time.Since(timeStart)

			m.observe(r, respWriter.StatusCode(), timeDiff)
		})
	}
}

func (c *Middleware) observe(r *http.Request, statusCode int, dur time.Duration) {
	if c.hist.h != nil {
		isErrorStr := strconv.FormatBool(IsStatusError(statusCode))

		c.hist.h.With(prometheus.Labels{
			"type":    r.Proto,
			"status":  strconv.Itoa(statusCode),
			"method":  r.Method,
			"addr":    metricAddr(r),
			"isError": isErrorStr,
		}).Observe(dur.Seconds())
	}
}

// metricAddr returns the value for the "addr" metric label. The matched route pattern
// (e.g. "/users/{id}") is preferred over the raw path: raw paths with parameters create
// one time series per distinct URL, which grows Prometheus memory unboundedly. The raw
// path is used only as fallback when the handler is not routed through a pattern-aware
// http.ServeMux.
func metricAddr(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

// Histogram ensures that when we call observe the request metric has been initialized correctly with NewPromHistogram
type Histogram struct {
	h *prometheus.HistogramVec
}

func NewPromHistogram(prefix string, buckets []float64, registry prometheus.Registerer) (Histogram, error) {
	if registry == nil {
		registry = prometheus.DefaultRegisterer
	}

	if len(buckets) == 0 {
		buckets = prometheus.DefBuckets
	}

	if prefix == "" {
		prefix = "requests"
	}

	histogram := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: prefix,
		Subsystem: "http",
		Name:      "duration_seconds",
		Help:      "Duration of HTTP requests for different paths, methods, status codes",
		Buckets:   buckets,
	},
		[]string{
			"type",
			"status",
			"method",
			"addr",
			"isError",
		},
	)
	if err := registry.Register(histogram); err != nil {
		return Histogram{}, fmt.Errorf("registering prometheus histogram: %w", err)
	}

	return Histogram{
		h: histogram,
	}, nil
}
