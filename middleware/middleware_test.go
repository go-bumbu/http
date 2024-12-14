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
		fmt.Fprint(w, message)
	})
}

func TestMiddlewareGenericErrs(t *testing.T) {
	tcs := []struct {
		name        string
		statusCode  int
		message     string
		genericErrs bool
		expect      string
	}{
		{
			name:        "simple error",
			statusCode:  500,
			message:     "DB connection broken",
			genericErrs: false,
			expect:      `DB connection broken`,
		},
		{
			name:        "use generic handlerMsg",
			statusCode:  500,
			message:     "DB connection broken",
			genericErrs: true,
			expect:      `Internal Server Error`,
		},
		{
			name:        "don't handle non errors",
			statusCode:  200,
			message:     "ok",
			genericErrs: true,
			expect:      "ok",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {

			m := middleware.New(middleware.Cfg{
				GenericErrs: tc.genericErrs,
			})

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
func TestJsonOutput(t *testing.T) {
	tcs := []struct {
		name        string
		statusCode  int
		message     string
		genericErrs bool
		expect      string
	}{
		{
			name:        "simple error",
			statusCode:  500,
			message:     "DB connection broken",
			genericErrs: false,
			expect:      `{"error":"DB connection broken","code":500}`,
		},
		{
			name:        "use generic handlerMsg",
			statusCode:  500,
			message:     "DB connection broken",
			genericErrs: true,
			expect:      `{"error":"Internal Server Error","code":500}`,
		},
		{
			name:        "don't handle non errors",
			statusCode:  200,
			message:     "ok",
			genericErrs: true,
			expect:      "ok",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {

			m := middleware.New(middleware.Cfg{
				GenericErrs: tc.genericErrs,
				JsonErrors:  true,
			})

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
