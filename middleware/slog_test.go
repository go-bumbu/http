package middleware_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-bumbu/http/middleware"
	"github.com/google/go-cmp/cmp"
)

func TestSlogMiddleware(t *testing.T) {
	tcs := []struct {
		name          string
		statusCode    int
		handlerMsg    string
		genericErr    bool
		expect        string
		expectPayload string
	}{
		{
			name:          "regular request",
			statusCode:    200,
			handlerMsg:    "ok",
			expect:        "INFO method=GET url=/metrics response-code=200 req-id= ",
			expectPayload: "ok",
		},
		{
			name:          "capture 4xx handlerMsg",
			statusCode:    401,
			handlerMsg:    "unauthorized",
			expect:        "INFO method=GET url=/metrics response-code=401 req-id= err-handlerMsg=unauthorized ",
			expectPayload: "unauthorized",
		},
		{
			name:          "capture error handlerMsg",
			statusCode:    500,
			handlerMsg:    "my db broke down",
			expect:        "ERROR method=GET url=/metrics response-code=500 req-id= err-handlerMsg=my db broke down ",
			expectPayload: "my db broke down",
		},
		{
			name:          "non generic errors logged",
			statusCode:    500,
			genericErr:    true,
			handlerMsg:    "my db broke down",
			expect:        "ERROR method=GET url=/metrics response-code=500 req-id= err-handlerMsg=my db broke down ",
			expectPayload: "Internal Server Error",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			th := testHandler(tc.statusCode, tc.handlerMsg)
			buf, logger := newMemSlog()

			m := middleware.New(middleware.Cfg{
				Logger:      logger,
				GenericErrs: tc.genericErr,
			})

			handler := m.Middleware(th)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/metrics", nil)
			handler.ServeHTTP(rec, req)
			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)

			// expect body still to be written to the http response
			respBody := string(body)
			if diff := cmp.Diff(respBody, tc.expectPayload); diff != "" {
				t.Errorf("unexpected value (-got +want)\n%s", diff)
			}

			// expect log messages wit certain information
			if diff := cmp.Diff(buf.String(), tc.expect); diff != "" {
				t.Errorf("unexpected value (-got +want)\n%s", diff)
			}

		})
	}
}

// InMemoryHandler is a custom slog.Handler implementation that writes logs to an in-memory buffer.
// minLevel gates records and writeAttr flattens grouped attrs into "group.key=value" form so
// tests can assert on grouped attributes emitted by the middleware.
type InMemoryHandler struct {
	Buffer   *bytes.Buffer
	minLevel slog.Level
}

func (h *InMemoryHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minLevel
}

// slog attributes that will be skipped in the test
var skipAttr = []string{
	"req-dur",
	"ip",
}

func (h *InMemoryHandler) Handle(_ context.Context, r slog.Record) error {
	var logMsg bytes.Buffer
	logMsg.WriteString(r.Level.String())
	logMsg.WriteString(" ")
	r.Attrs(func(attr slog.Attr) bool {
		writeAttr(&logMsg, "", attr)
		return true
	})
	logMsg.WriteString(r.Message)
	h.Buffer.Write(logMsg.Bytes())
	return nil
}

// writeAttr renders an attr, recursing into groups so a grouped attr like
// slog.Group("req-headers", slog.String("Authorization", "[REDACTED]")) becomes
// "req-headers.Authorization=[REDACTED] " in the buffer.
func writeAttr(buf *bytes.Buffer, prefix string, attr slog.Attr) {
	if slices.Contains(skipAttr, attr.Key) {
		return
	}
	key := attr.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, sub := range attr.Value.Group() {
			writeAttr(buf, key, sub)
		}
		return
	}
	buf.WriteString(key + "=" + attr.Value.String() + " ")
}

func (h *InMemoryHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *InMemoryHandler) WithGroup(name string) slog.Handler   { return h }

func newMemSlog() (*bytes.Buffer, *slog.Logger) {
	return newMemSlogLevel(slog.LevelDebug)
}

func newMemSlogLevel(min slog.Level) (*bytes.Buffer, *slog.Logger) {
	buffer := &bytes.Buffer{}
	handler := &InMemoryHandler{Buffer: buffer, minLevel: min}
	logger := slog.New(handler)
	return buffer, logger
}

// TestLogger verifies that log messages match expected output.
func TestMemoryHandler(t *testing.T) {
	buf, logger := newMemSlog()
	logger.Info("test handlerMsg", slog.String("key", "value"))

	expected := "INFO key=value test handlerMsg"
	if !bytes.Contains(buf.Bytes(), []byte(expected)) {
		t.Errorf("expected log to contain: %q, got: %q", expected, buf.String())
	}
}

// testHandlerWithRespHeaders writes a fixed response header before returning 200
// so tests can assert on resp-headers in the debug log line.
func testHandlerWithRespHeaders(body string, respHeaders map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range respHeaders {
			w.Header().Set(k, v)
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestLogHeaders(t *testing.T) {
	tcs := []struct {
		name               string
		logHeaders         bool
		extraRedact        []string
		disableRedaction   bool
		reqHeaders         map[string][]string
		respHeaders        map[string]string
		mustContain        []string
		mustNotContain     []string
	}{
		{
			name:           "disabled by default — no debug line",
			logHeaders:     false,
			reqHeaders:     map[string][]string{"Authorization": {"Bearer abc"}},
			mustNotContain: []string{"DEBUG", "req-headers", "resp-headers"},
		},
		{
			name:       "enabled — redacts Authorization, keeps Content-Type, logs resp headers",
			logHeaders: true,
			reqHeaders: map[string][]string{
				"Authorization": {"Bearer abc"},
				"Content-Type":  {"application/json"},
			},
			respHeaders: map[string]string{"X-Trace-Id": "t-1"},
			mustContain: []string{
				"DEBUG",
				"req-headers.Authorization=[REDACTED]",
				"req-headers.Content-Type=application/json",
				"resp-headers.X-Trace-Id=t-1",
			},
			mustNotContain: []string{"Bearer abc"},
		},
		{
			name:        "ExtraRedactHeaders — case-insensitive match on added header",
			logHeaders:  true,
			extraRedact: []string{"x-tenant-secret"},
			reqHeaders:  map[string][]string{"X-Tenant-Secret": {"super-secret"}},
			mustContain: []string{"req-headers.X-Tenant-Secret=[REDACTED]"},
			mustNotContain: []string{"super-secret"},
		},
		{
			name:             "DisableRedaction — Authorization printed verbatim",
			logHeaders:       true,
			disableRedaction: true,
			reqHeaders:       map[string][]string{"Authorization": {"Bearer abc"}},
			mustContain:      []string{"req-headers.Authorization=Bearer abc"},
			mustNotContain:   []string{"[REDACTED]"},
		},
		{
			name:       "multi-value Cookie still redacted; multi-value non-sensitive joined with comma-space",
			logHeaders: true,
			reqHeaders: map[string][]string{
				"Cookie":          {"a=1", "b=2"},
				"Accept-Encoding": {"gzip", "deflate"},
			},
			mustContain: []string{
				"req-headers.Cookie=[REDACTED]",
				"req-headers.Accept-Encoding=gzip, deflate",
			},
			mustNotContain: []string{"a=1", "b=2"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			buf, logger := newMemSlog()
			th := testHandlerWithRespHeaders("ok", tc.respHeaders)

			m := middleware.New(middleware.Cfg{
				Logger:             logger,
				LogHeaders:         tc.logHeaders,
				ExtraRedactHeaders: tc.extraRedact,
				DisableRedaction:   tc.disableRedaction,
			})

			handler := m.Middleware(th)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/", nil)
			for k, vs := range tc.reqHeaders {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			handler.ServeHTTP(rec, req)

			got := buf.String()
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("log missing %q\nfull log:\n%s", want, got)
				}
			}
			for _, avoid := range tc.mustNotContain {
				if strings.Contains(got, avoid) {
					t.Errorf("log unexpectedly contained %q\nfull log:\n%s", avoid, got)
				}
			}
		})
	}
}

func TestLogHeaders_LoggerNotDebugEnabled(t *testing.T) {
	buf, logger := newMemSlogLevel(slog.LevelInfo)
	th := testHandlerWithRespHeaders("ok", nil)

	m := middleware.New(middleware.Cfg{Logger: logger, LogHeaders: true})
	handler := m.Middleware(th)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer abc")
	handler.ServeHTTP(rec, req)

	got := buf.String()
	if strings.Contains(got, "DEBUG") || strings.Contains(got, "req-headers") {
		t.Errorf("expected no debug record when logger debug-disabled, got:\n%s", got)
	}
}
