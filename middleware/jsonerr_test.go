package middleware_test

import (
	"github.com/andresbott/go-carbon/libs/http/middleware"
	"github.com/google/go-cmp/cmp"
	"io"
	"net/http/httptest"
	"testing"
)

func TestJsonErrMiddleware(t *testing.T) {
	tcs := []struct {
		name       string
		statusCode int
		message    string
		genericMsg bool
		expect     string
	}{
		{
			name:       "simple error",
			statusCode: 500,
			message:    "DB connection broken",
			genericMsg: true,
			expect:     `{"Error":"Internal Server Error","Code":500}`,
		},
		{
			name:       "use generic message",
			statusCode: 500,
			message:    "DB connection broken",
			genericMsg: false,
			expect:     `{"Error":"DB connection broken","Code":500}`,
		},
		{
			name:       "don't handle non errors",
			statusCode: 200,
			message:    "ok",
			genericMsg: true,
			expect:     "ok",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			th := testHandler(tc.statusCode, tc.message)
			jsErrHndlr := middleware.JsonErrMiddleware(th, tc.genericMsg)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/metrics", nil)
			jsErrHndlr.ServeHTTP(rec, req)
			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)
			got := string(body)

			if diff := cmp.Diff(got, tc.expect); diff != "" {
				t.Errorf("unexpected value (-got +want)\n%s", diff)
			}

		})
	}
}
