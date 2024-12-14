package middleware_test

import (
	"github.com/go-bumbu/http/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"io"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromMiddleware(t *testing.T) {
	tcs := []struct {
		name         string
		requests     func(h http.Handler)
		metricPrefix string

		statusCode    int
		expectedLines []string
	}{
		{
			name: "simple test",
			requests: func(h http.Handler) {
				r := httptest.NewRequest("GET", "/bla", nil)
				r2 := httptest.NewRequest("POST", "/ble/bli", nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				h.ServeHTTP(rec, r2)
			},
			statusCode: 200,
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
				r := httptest.NewRequest("GET", "/bla", nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
			},
			metricPrefix: "ehmm",
			statusCode:   200,
			expectedLines: []string{
				`ehmm_http_duration_seconds_bucket{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1",le="0.005"} 1`,
				`ehmm_http_duration_seconds_bucket{addr="/bla",isError="false",method="GET",status="200",type="HTTP/1.1",le="0.01"} 1`,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {

			reg := prometheus.NewRegistry()
			hist := middleware.NewPromHistogram(tc.metricPrefix, nil, reg)

			m := middleware.New(middleware.Cfg{
				JsonErrors: false,
				Logger:     nil,
				Histogram:  hist,
			})

			promHandler := m.Middleware(testHandler(tc.statusCode, "ok"))
			tc.requests(promHandler)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/metrics", nil)

			promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
			resp := rec.Result()

			body, _ := io.ReadAll(resp.Body)

			respBody := string(body)

			//fmt.Print(respBody)
			for _, line := range tc.expectedLines {
				if !strings.Contains(respBody, line) {
					t.Errorf("response does not contains expected line: %s", line)
				}
			}

		})
	}
}
