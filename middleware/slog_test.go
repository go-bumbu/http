package middleware_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
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
// minLevel gates records; group (set via WithGroup) is used to prefix attr keys so tests can
// assert on flat "group.key=value" strings for grouped attributes emitted by the middleware.
type InMemoryHandler struct {
	Buffer   *bytes.Buffer
	minLevel slog.Level
	group    string
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
