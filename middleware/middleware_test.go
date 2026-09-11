package middleware_test

import (
	"fmt"
	"github.com/go-bumbu/http/middleware"
	"github.com/google/go-cmp/cmp"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testHandler(statusCode int, message string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		_, _ = fmt.Fprint(w, message)
	})
}

func TestMiddlewareErrorPassthrough(t *testing.T) {
	tcs := []struct {
		name       string
		statusCode int
		message    string
		expect     string
	}{
		{
			name:       "error body passes through",
			statusCode: 500,
			message:    "DB connection broken",
			expect:     `DB connection broken`,
		},
		{
			name:       "success passes through",
			statusCode: 200,
			message:    "ok",
			expect:     "ok",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {

			m := middleware.New(middleware.Cfg{})

			th := testHandler(tc.statusCode, tc.message)
			handler := m.Middleware(th)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/metrics", nil)
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)
			got := string(body)

			if diff := cmp.Diff(got, tc.expect); diff != "" {
				t.Errorf("unexpected value (-got +want)\n%s", diff)
			}

		})
	}
}
