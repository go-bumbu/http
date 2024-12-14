package middleware

import (
	"github.com/prometheus/client_golang/prometheus"
	"net/http"
	"strconv"
	"time"
)

func (c *Middleware) observe(r *http.Request, statusCode int, dur time.Duration) {
	if c.hist.h != nil {
		isErrorStr := strconv.FormatBool(IsStatusError(statusCode))

		// todo don't print all paths, this creates too much cardinality
		c.hist.h.With(prometheus.Labels{
			"type":    r.Proto,
			"status":  strconv.Itoa(statusCode),
			"method":  r.Method,
			"addr":    r.URL.Path,
			"isError": isErrorStr,
		}).Observe(dur.Seconds())
	}
}

type Histogram struct {
	h *prometheus.HistogramVec
}

func NewPromHistogram(prefix string, buckets []float64, registry prometheus.Registerer) Histogram {
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
	registry.MustRegister(histogram)

	return Histogram{
		h: histogram,
	}
}
