// Package metrics provides a Prometheus-backed implementation of
// middleware.Observer plus a standalone metrics middleware. It is the only
// package in this module that imports the Prometheus client, so consumers that
// import only middleware do not compile or link Prometheus.
package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-bumbu/http/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// Histogram wraps a registered Prometheus HistogramVec used to record request
// durations. Create one with NewPromHistogram; the zero value records nothing.
type Histogram struct {
	h *prometheus.HistogramVec
}

// NewPromHistogram registers and returns a request-duration Histogram. An empty
// prefix defaults to "requests"; empty buckets default to prometheus.DefBuckets;
// a nil registry defaults to prometheus.DefaultRegisterer.
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

// NewObserver adapts a Histogram to middleware.Observer. It returns nil when the
// Histogram is the zero value (no histogram registered), so passing the result
// straight into middleware.Cfg.Metrics disables metrics — matching the previous
// "empty histogram = no metrics" behaviour.
func NewObserver(hist Histogram) middleware.Observer {
	if hist.h == nil {
		return nil
	}
	return observer(hist)
}

type observer struct {
	h *prometheus.HistogramVec
}

func (o observer) Observe(status int, r *http.Request, d time.Duration) {
	o.h.With(prometheus.Labels{
		"type":    r.Proto,
		"status":  strconv.Itoa(status),
		"method":  r.Method,
		"addr":    metricAddr(r),
		"isError": strconv.FormatBool(middleware.IsStatusError(status)),
	}).Observe(d.Seconds())
}

// Middleware returns a standalone middleware that records Prometheus request
// duration metrics. A zero Histogram yields a transparent passthrough.
func Middleware(hist Histogram) func(http.Handler) http.Handler {
	obs := NewObserver(hist)
	if obs == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := middleware.NewWriter(w, false)
			next.ServeHTTP(sw, r)
			obs.Observe(sw.StatusCode(), r, time.Since(start))
		})
	}
}

// metricAddr returns the value for the "addr" metric label. The matched route
// pattern (e.g. "/users/{id}") is preferred over the raw path: raw paths with
// parameters create one time series per distinct URL, which grows Prometheus
// memory unboundedly. The raw path is used only as a fallback when the handler is
// not routed through a pattern-aware http.ServeMux.
func metricAddr(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}
