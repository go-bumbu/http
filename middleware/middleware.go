package middleware

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-bumbu/http/lib/limitio"
)

type Cfg struct {
	JsonErrors   bool
	GenericErrs  bool // print generic error messages instead of the actual one
	PanicRecover bool
	Logger       *slog.Logger
	PromHisto    Histogram

	// LogHeaders, when true, causes the middleware to emit one additional
	// log record per request at slog.LevelDebug containing request and
	// response headers. The record is only emitted when the configured
	// slog.Logger is enabled for LevelDebug.
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
	m := Middleware{
		jsonErrors:       cfg.JsonErrors,
		genericErrs:      cfg.GenericErrs,
		panicRecover:     cfg.PanicRecover,
		hist:             cfg.PromHisto,
		logger:           cfg.Logger,
		logHeaders:       cfg.LogHeaders,
		disableRedaction: cfg.DisableRedaction,
		redact:           newRedactSet(cfg.ExtraRedactHeaders),
	}
	return &m
}

// Middleware is intended perform common actions done by a production http server, it has several configuration flags:
//   - JsonErrors: if set to true it will intercept all error responses (status >= 400, see IsStatusError),
//     read the response error handlerMsg and wrap it into a json file, this is useful for APIs
//   - GenericErrs: if set to true the error handlerMsg responded to the en user is a generic handlerMsg based on the
//     response code instead of the original error handlerMsg, the original error will still be logged.
//
// NOTE: both JsonErrors and GenericErrs only intercept error responses (>= 400). Success codes like
// 200, 204, 206 etc. pass through unmodified, as do 1xx informational responses.
// Handlers that stream (flush before the response is complete) or hijack the connection
// are never modified.
//
//   - Histogram: use NewPromHistogram to create an histogram used to capture prometheus metrics about every request
//     if left empty, no prometheus metric will be captured
type Middleware struct {
	jsonErrors       bool
	genericErrs      bool
	panicRecover     bool
	hist             Histogram
	logger           *slog.Logger
	logHeaders       bool
	disableRedaction bool
	redact           redactSet
}

// Middleware is an HTTP middleware that checks the Config and applies logic based on it.
func (c *Middleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeStart := time.Now()
		// teeOnErr: when we won't modify the body (no genericErrs, no jsonErrors), tee so the
		// client receives it during e.g. reverse proxy copy—avoids indefinite hang on 401.
		teeOnErr := !c.genericErrs && !c.jsonErrors
		respWriter := NewWriter(w, true, teeOnErr)

		if c.panicRecover {
			defer func() {
				if rec := recover(); rec != nil {
					if isAbort(rec) {
						// net/http's sentinel to abort the response so the client
						// sees a truncated reply (ReverseProxy panics with it when
						// the upstream dies mid-copy). Swallowing it would make the
						// truncated response look complete; hand it back to net/http.
						c.observe(r, respWriter.StatusCode(), time.Since(timeStart))
						panic(rec)
					}
					c.handlePanic(r, respWriter, rec, debug.Stack())
				}
				c.finalize(r, respWriter, timeStart)
			}()
		}

		next.ServeHTTP(respWriter, r)

		if !c.panicRecover {
			c.finalize(r, respWriter, timeStart)
		}
	})
}

// isAbort reports whether a recovered panic value is http.ErrAbortHandler, the stdlib
// sentinel that means "abort this response, let the client detect the truncation".
func isAbort(rec any) bool {
	err, ok := rec.(error)
	return ok && errors.Is(err, http.ErrAbortHandler)
}

// handlePanic logs a recovered panic and, when the response has not started streaming,
// turns it into a 500 response. http.ErrAbortHandler must not reach here (see isAbort):
// it is the stdlib's sentinel for deliberately aborting the response so the client
// detects truncation; recovering it would make a truncated reply look complete.
func (c *Middleware) handlePanic(r *http.Request, respWriter *StatWriter, rec any, stack []byte) {
	if c.logger != nil {
		c.logger.Error("panic recovered",
			slog.String("method", r.Method),
			slog.String("url", r.RequestURI),
			slog.String("panic", fmt.Sprint(rec)),
			slog.String("stack", string(stack)),
		)
	}
	// Only synthesise a 500 when the response is still ours to write: after a hijack or
	// a flush the client already has bytes, and appending an error body would corrupt
	// the stream.
	if respWriter.Streaming() {
		return
	}
	respWriter.WriteHeader(http.StatusInternalServerError)
	_, _ = respWriter.Write([]byte(http.StatusText(http.StatusInternalServerError)))
}

func (c *Middleware) finalize(r *http.Request, respWriter *StatWriter, timeStart time.Time) {
	timeDiff := time.Since(timeStart)

	errMsg := c.getErrMsg(respWriter.statusCode, respWriter.buf)
	c.log(r, respWriter.StatusCode(), errMsg, timeDiff)
	c.logHeadersDebug(r, respWriter.Header())

	if c.genericErrs {
		errMsg = http.StatusText(respWriter.StatusCode())
	}

	if respWriter.canReplaceBody() {
		if c.jsonErrors {
			b := jsonErrBytes(errMsg, respWriter.StatusCode())
			writeReplacementBody(respWriter, "application/json", b)
		} else {
			writeReplacementBody(respWriter, "text/plain", []byte(errMsg))
		}
	} else {
		respWriter.flushHeader()
	}

	c.observe(r, respWriter.StatusCode(), timeDiff)
}

// getErrMsg returns the error handlerMsg in case of an error response or empty string
func (c *Middleware) getErrMsg(code int, buf *limitio.LimitedBuf) string {
	if !IsStatusError(code) {
		return ""
	}

	msgB, err := io.ReadAll(buf)
	if err != nil && c.logger != nil {
		c.logger.Error("error while reading buffer error handlerMsg:", slog.Any("err", err))
	}
	msg := strings.Trim(string(msgB), "\n")
	if buf.Truncated() {
		msg += " [truncated]"
	}
	return msg
}
