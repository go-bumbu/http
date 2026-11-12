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

	// ClientIP extracts the client address logged as the "ip" field. When nil it defaults to
	// ClientIPDirect: the direct peer, ignoring forwarding headers, which a client could forge.
	// Behind a reverse proxy, use ClientIPFromRealIP or ClientIPFromXFF with the proxy's
	// address range (or ClientIPFromXFFHops), so the real client is logged.
	ClientIP func(*http.Request) string

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

	// RouteGroups lists URL path prefixes that collapse every route beneath them into one
	// metric route: with "/api", "GET /api/apple" and "GET /api/banana" both record as
	// "GET /api". Prefixes match whole path segments and the longest one wins. Entries are
	// paths only: a leading method ("GET /api") is stripped, a host is not supported, a
	// leading slash is added, repeated or trailing slashes are dropped, and a bare "/" is
	// ignored. Only used when Metrics is set.
	RouteGroups []string
}

func New(cfg Cfg) *Middleware {
	requestID := cfg.RequestID
	if requestID == nil {
		requestID = defaultRequestID
	}
	clientIP := cfg.ClientIP
	if clientIP == nil {
		clientIP = ClientIPDirect
	}
	m := Middleware{
		clientIP:         clientIP,
		panicRecover:     cfg.PanicRecover,
		metrics:          cfg.Metrics,
		logger:           cfg.Logger,
		logHeaders:       cfg.LogHeaders,
		disableRedaction: cfg.DisableRedaction,
		redact:           newRedactSet(cfg.ExtraRedactHeaders),
		requestID:        requestID,
		routeGroups:      newRouteGroups(cfg.RouteGroups),
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
	clientIP         func(*http.Request) string
	routeGroups      routeGroups
}

// Wrap returns an http.Handler that runs next with the configured request logging, metrics,
// and panic recovery. It never modifies the response body; see the Middleware type doc for
// the streaming, error-tee, and passthrough guarantees.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// With metrics on, pass a copy carrying the route slot down the chain. ServeMux sets
		// r.Pattern on the request it receives, so every step below (finalize, observe) must
		// use this copy. restore hands the pattern back to the caller's request; deferred
		// first, it runs last: after panic recovery, and while an abort panic unwinds.
		r, slot, restore := m.withRouteSlot(r)
		defer restore()
		timeStart := time.Now()
		// The middleware never modifies the body; tee error responses so the client still
		// receives them during e.g. a reverse-proxy copy—avoids an indefinite hang on 401.
		respWriter := NewWriter(w)

		if m.panicRecover {
			defer func() {
				if rec := recover(); rec != nil {
					m.recovered(r, slot, respWriter, timeStart, rec)
				}
				m.finalize(r, slot, respWriter, timeStart)
			}()
		}

		next.ServeHTTP(respWriter, r)

		if !m.panicRecover {
			m.finalize(r, slot, respWriter, timeStart)
		}
	})
}

// recovered handles a panic recovered from the handler. It returns normally only when it
// turned the panic into a 500 response; otherwise it re-panics with http.ErrAbortHandler after
// recording the request.
func (m *Middleware) recovered(r *http.Request, slot *routeSlot, respWriter *StatWriter, timeStart time.Time, rec any) {
	if isAbort(rec) {
		// net/http's sentinel to abort the response so the client sees a truncated reply
		// (ReverseProxy panics with it when the upstream dies mid-copy). Swallowing it would
		// make the truncated response look complete; hand it back to net/http.
		m.observe(r, slot, respWriter.StatusCode(), time.Since(timeStart))
		panic(rec)
	}
	if !m.handlePanic(r, respWriter, rec, debug.Stack()) {
		// The response had started: the client already holds a status and maybe part of the
		// body. Finishing normally would make the truncated reply look complete, so record it
		// and abort the connection, as net/http does for an unrecovered panic.
		m.finalize(r, slot, respWriter, timeStart)
		panic(http.ErrAbortHandler)
	}
}

// isAbort reports whether a recovered panic value is http.ErrAbortHandler, the stdlib
// sentinel that means "abort this response, let the client detect the truncation".
func isAbort(rec any) bool {
	err, ok := rec.(error)
	return ok && errors.Is(err, http.ErrAbortHandler)
}

// handlePanic logs a recovered panic and, when the response has not started, turns it into a
// 500 response and returns true. It returns false when the response had already started; the
// caller must then abort the connection so the client detects the truncation.
// http.ErrAbortHandler must not reach here (see isAbort): it is the stdlib's sentinel for
// deliberately aborting the response; recovering it would make a truncated reply look complete.
func (m *Middleware) handlePanic(r *http.Request, respWriter *StatWriter, rec any, stack []byte) bool {
	if m.logger != nil {
		m.logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
			slog.String("method", r.Method),
			slog.String("url", logURL(r)),
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
		return false
	}
	respWriter.WriteHeader(http.StatusInternalServerError)
	_, _ = respWriter.Write([]byte(http.StatusText(http.StatusInternalServerError)))
	return true
}

func (m *Middleware) finalize(r *http.Request, slot *routeSlot, respWriter *StatWriter, timeStart time.Time) {
	timeDiff := time.Since(timeStart)

	errMsg := getErrMsg(respWriter.statusCode, respWriter.buf)
	m.log(r, respWriter.StatusCode(), errMsg, timeDiff)
	m.logHeadersDebug(r, respWriter.Header())

	respWriter.flushHeader()

	m.observe(r, slot, respWriter.StatusCode(), timeDiff)
}

// observe settles the request's route (read back by Observers via Route) and records the
// metric. r must be the request copy carrying slot, so r.Pattern reflects a ServeMux match.
func (m *Middleware) observe(r *http.Request, slot *routeSlot, statusCode int, dur time.Duration) {
	if m.metrics != nil {
		m.resolveRoute(r, slot)
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
