// Package metrics provides a Prometheus-backed implementation of
// middleware.Observer. It is the only package in this module that imports the
// Prometheus client, so consumers that import only middleware do not compile or
// link Prometheus.
package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-bumbu/http/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// Cfg configures an Observer. Every field is optional; each zero value falls
// back to a sane default (see the field docs), so metrics.Cfg{} is a valid,
// fully-defaulted configuration.
type Cfg struct {
	// Prefix is the metric namespace; "" becomes "requests".
	Prefix string
	// Buckets are the histogram's duration buckets, in strictly increasing
	// order; empty becomes prometheus.DefBuckets. NewObserver errors if they are
	// not strictly increasing.
	Buckets []float64
	// Registry is where the histogram registers; nil becomes
	// prometheus.DefaultRegisterer.
	Registry prometheus.Registerer
}

// NewObserver registers a request-duration histogram and returns a
// middleware.Observer that records one observation per request. Wire the result
// into middleware.Cfg.Metrics.
//
// It returns an error if cfg.Buckets are not strictly increasing, or if the
// histogram cannot be registered (for example a duplicate registration). On
// error the returned Observer is nil and must not be used; to disable metrics,
// leave middleware.Cfg.Metrics nil or pass NopObserver rather than a nil
// interface.
func NewObserver(cfg Cfg) (middleware.Observer, error) {
	registry := cfg.Registry
	if registry == nil {
		registry = prometheus.DefaultRegisterer
	}
	buckets := cfg.Buckets
	if len(buckets) == 0 {
		buckets = prometheus.DefBuckets
	}
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "requests"
	}

	// Prometheus validates bucket ordering lazily on the first Observe, i.e. in
	// the request hot path. Check here so a mis-sorted slice fails fast at
	// construction instead of panicking later from inside Observe.
	for i := 1; i < len(buckets); i++ {
		if buckets[i] <= buckets[i-1] {
			return nil, fmt.Errorf("buckets must be strictly increasing: buckets[%d]=%g <= buckets[%d]=%g",
				i, buckets[i], i-1, buckets[i-1])
		}
	}

	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: prefix,
		Subsystem: "http",
		Name:      "duration_seconds",
		Help:      "Duration of HTTP requests by protocol, status, method and route.",
		Buckets:   buckets,
	},
		[]string{"type", "status", "method", "addr"},
	)
	if err := registry.Register(h); err != nil {
		return nil, fmt.Errorf("registering prometheus histogram: %w", err)
	}

	return observer{h: h}, nil
}

// NopObserver returns a middleware.Observer that records nothing. Use it to
// disable metrics explicitly instead of threading a nil middleware.Observer,
// which would panic if a caller stored it and later called Observe.
func NopObserver() middleware.Observer { return nopObserver{} }

type nopObserver struct{}

func (nopObserver) Observe(int, *http.Request, time.Duration) {}

// observer records request durations into a Prometheus HistogramVec. Its label
// values are deliberately bounded (see normalizeMethod and metricAddr) so a
// client cannot explode time-series cardinality and exhaust memory.
type observer struct {
	h *prometheus.HistogramVec
}

func (o observer) Observe(status int, r *http.Request, d time.Duration) {
	o.h.With(prometheus.Labels{
		"type":   r.Proto,
		"status": strconv.Itoa(status),
		"method": normalizeMethod(r.Method),
		"addr":   metricAddr(r),
	}).Observe(d.Seconds())
}

// normalizeMethod maps r.Method onto the fixed set of standard HTTP methods,
// collapsing anything else to "other". net/http accepts any RFC 9110 token as a
// method, so recording r.Method verbatim would let a client mint unbounded
// distinct label values — one permanent time series each — and exhaust memory.
func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect,
		http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "other"
	}
}

// metricAddr returns the value for the "addr" label. It uses the matched route
// pattern (e.g. "GET /users/{id}"), a bounded set, and reports "unmatched" for
// requests that did not match a pattern-aware http.ServeMux route (Go 1.22+).
// The raw r.URL.Path is attacker-controlled and would create one time series
// per distinct URL, so it is deliberately never used as a label value.
func metricAddr(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}
