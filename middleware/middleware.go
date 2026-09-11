package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// Observer records a per-request metric. The combined Middleware (and any code
// using it) calls Observe exactly once per request with the final status code,
// the request, and the total handler duration. Implementations live outside this
// package — see github.com/go-bumbu/http/middleware/metrics — which is what keeps
// middleware free of any metrics-backend dependency.
type Observer interface {
	Observe(status int, r *http.Request, d time.Duration)
}

type Cfg struct {
	PanicRecover bool
	Logger       Logger
	Metrics      Observer

	// RequestID extracts the correlation id logged as the "req-id" field. When
	// nil it defaults to reading the Request-Id request header; supply your own
	// to read a different header (X-Request-Id, X-Correlation-Id, traceparent, …)
	// or the request context. An empty result logs an empty req-id.
	RequestID func(*http.Request) string

	// LogHeaders, when true, causes the middleware to emit one additional
	// log record per request at slog.LevelDebug containing request and
	// response headers. The record is only emitted when the configured
	// Logger is enabled for LevelDebug.
	LogHeaders bool

	// ExtraRedactHeaders lists additional header names whose values are
	// replaced with "[REDACTED]" in the debug header log. Matching is
	// case-insensitive against canonicalised header keys and is appended
	// to the built-in default list. Ignored when DisableRedaction is true.
	ExtraRedactHeaders []string

	// DisableRedaction, when true, logs header values verbatim. The
	// built-in redact list and ExtraRedactHeaders are both ignored.
	// Intended for local debugging only.
	DisableRedaction bool
}

func New(cfg Cfg) *Middleware {
	requestID := cfg.RequestID
	if requestID == nil {
		requestID = defaultRequestID
	}
	m := Middleware{
		panicRecover:     cfg.PanicRecover,
		metrics:          cfg.Metrics,
		logger:           cfg.Logger,
		logHeaders:       cfg.LogHeaders,
		disableRedaction: cfg.DisableRedaction,
		redact:           newRedactSet(cfg.ExtraRedactHeaders),
		requestID:        requestID,
	}
	return &m
}

// Middleware is intended to perform common actions done by a production http server. It wraps a
// handler to add request logging, metrics, and panic recovery. It never modifies the
// response body: error responses (>= 400) are forwarded to the client and their bodies captured
// for logging.
//
// NOTE: Success codes like 200, 204, 206 etc. pass through unmodified, as do 1xx informational
// responses. Handlers that stream (flush before the response is complete) or hijack the
// connection are never modified.
//
//   - Metrics: supply a middleware.Observer (e.g. metrics.NewObserver from
//     github.com/go-bumbu/http/middleware/metrics) to record a metric per request;
//     if nil, no metric is captured.
type Middleware struct {
	panicRecover     bool
	metrics          Observer
	logger           Logger
	logHeaders       bool
	disableRedaction bool
	redact           redactSet
	requestID        func(*http.Request) string
}

// Wrap returns an http.Handler that runs next with the configured request logging, metrics,
// and panic recovery. It never modifies the response body; see the Middleware type doc for
// the streaming, error-tee, and passthrough guarantees.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeStart := time.Now()
		// The middleware never modifies the body; tee error responses so the client still
		// receives them during e.g. a reverse-proxy copy—avoids an indefinite hang on 401.
		respWriter := NewWriter(w)

		if m.panicRecover {
			defer func() {
				if rec := recover(); rec != nil {
					if isAbort(rec) {
						// net/http's sentinel to abort the response so the client
						// sees a truncated reply (ReverseProxy panics with it when
						// the upstream dies mid-copy). Swallowing it would make the
						// truncated response look complete; hand it back to net/http.
						m.observe(r, respWriter.StatusCode(), time.Since(timeStart))
						panic(rec)
					}
					m.handlePanic(r, respWriter, rec, debug.Stack())
				}
				m.finalize(r, respWriter, timeStart)
			}()
		}

		next.ServeHTTP(respWriter, r)

		if !m.panicRecover {
			m.finalize(r, respWriter, timeStart)
		}
	})
}

// isAbort reports whether a recovered panic value is http.ErrAbortHandler, the stdlib
// sentinel that means "abort this response, let the client detect the truncation".
func isAbort(rec any) bool {
	err, ok := rec.(error)
	return ok && errors.Is(err, http.ErrAbortHandler)
}

// handlePanic logs a recovered panic and, when the response has not started, turns it into a
// 500 response. http.ErrAbortHandler must not reach here (see isAbort): it is the stdlib's
// sentinel for deliberately aborting the response so the client detects truncation; recovering
// it would make a truncated reply look complete.
func (m *Middleware) handlePanic(r *http.Request, respWriter *StatWriter, rec any, stack []byte) {
	if m.logger != nil {
		m.logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
			slog.String("method", r.Method),
			slog.String("url", r.RequestURI),
			slog.String("panic", fmt.Sprint(rec)),
			slog.String("stack", string(stack)),
		)
	}
	// Only synthesise a 500 while the response is still ours to write. After a flush or hijack
	// the client already holds bytes (Streaming); and once the status has been committed
	// (Started) — e.g. a handler that wrote part of a body and then panicked — appending our
	// error text would corrupt that response under its already-sent status, exactly as
	// net/http declines to do.
	if respWriter.Streaming() || respWriter.Started() {
		return
	}
	respWriter.WriteHeader(http.StatusInternalServerError)
	_, _ = respWriter.Write([]byte(http.StatusText(http.StatusInternalServerError)))
}

func (m *Middleware) finalize(r *http.Request, respWriter *StatWriter, timeStart time.Time) {
	timeDiff := time.Since(timeStart)

	errMsg := getErrMsg(respWriter.statusCode, respWriter.buf)
	m.log(r, respWriter.StatusCode(), errMsg, timeDiff)
	m.logHeadersDebug(r, respWriter.Header())

	respWriter.flushHeader()

	m.observe(r, respWriter.StatusCode(), timeDiff)
}

func (m *Middleware) observe(r *http.Request, statusCode int, dur time.Duration) {
	if m.metrics != nil {
		m.metrics.Observe(statusCode, r, dur)
	}
}

// getErrMsg returns the captured error-response body for logging, or "" for a non-error
// status. The body comes from the capped log buffer (bufMaxBytes); a truncated capture is
// flagged inline with " [truncated]".
func getErrMsg(code int, buf *limitBuf) string {
	if !IsStatusError(code) {
		return ""
	}
	msg := strings.Trim(buf.String(), "\n")
	if buf.Truncated() {
		msg += " [truncated]"
	}
	return msg
}
