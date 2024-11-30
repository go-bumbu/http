package middleware

import (
	"github.com/prometheus/client_golang/prometheus"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type Histogram struct {
	h *prometheus.HistogramVec
}

func NewHistogram(prefix string, buckets []float64, registry prometheus.Registerer) Histogram {
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

	return Histogram{h: histogram}
}

func PromMiddleware(next http.Handler, histogram Histogram) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeStart := time.Now()
		respWriter := NewWriter(w, false)
		// serve the request
		next.ServeHTTP(respWriter, r)
		// get the duration
		timeDiff := time.Since(timeStart)
		observe(histogram, r, respWriter.StatusCode(), timeDiff)
	})
}

func PromLogMiddleware(next http.Handler, histogram Histogram, l *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeStart := time.Now()
		respWriter := NewWriter(w, false)
		// serve the request
		next.ServeHTTP(respWriter, r)
		// get the duration
		timeDiff := time.Since(timeStart)
		// log the request
		log(l, r, respWriter.StatusCode(), timeDiff)
		// add prometheus metric
		observe(histogram, r, respWriter.StatusCode(), timeDiff)
	})
}

func observe(histogram Histogram, r *http.Request, statusCode int, dur time.Duration) {
	isErrorStr := strconv.FormatBool(IsStatusError(statusCode))

	// todo don't print all paths, this creates too much cardinality
	histogram.h.With(prometheus.Labels{
		"type":    r.Proto,
		"status":  strconv.Itoa(statusCode),
		"method":  r.Method,
		"addr":    r.URL.Path,
		"isError": isErrorStr,
	}).Observe(dur.Seconds())
}
