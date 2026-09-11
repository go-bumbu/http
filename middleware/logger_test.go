package middleware_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/go-bumbu/http/middleware"
)

// spyLogger is a Logger implementation that is deliberately NOT *slog.Logger,
// so a passing test proves the middleware depends on the interface, not the
// concrete type.
type spyLogger struct{ records int }

func (s *spyLogger) Enabled(context.Context, slog.Level) bool                   { return true }
func (s *spyLogger) LogAttrs(context.Context, slog.Level, string, ...slog.Attr) { s.records++ }

func TestLogging_AcceptsNonSlogLogger(t *testing.T) {
	spy := &spyLogger{}
	h := middleware.New(middleware.Cfg{Logger: spy}).Wrap(testHandler(200, "ok"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if spy.records == 0 {
		t.Fatal("expected the custom Logger to receive a record")
	}
}
