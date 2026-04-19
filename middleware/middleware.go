package middleware

import (
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
}

func New(cfg Cfg) *Middleware {
	m := Middleware{
		jsonErrors:   cfg.JsonErrors,
		genericErrs:  cfg.GenericErrs,
		panicRecover: cfg.PanicRecover,
		hist:         cfg.PromHisto,
		logger:       cfg.Logger,
	}
	return &m
}

// Middleware is intended perform common actions done by a production http server, it has several configuration flags:
//   - JsonErrors: if set to true it will intercept all error responses (status < 200 or >= 400), read the response
//     error handlerMsg and wrap it into a json file, this is useful for APIs
//   - GenericErrs: if set to true the error handlerMsg responded to the en user is a generic handlerMsg based on the
//     response code instead of the original error handlerMsg, the original error will still be logged.
//
// NOTE: both JsonErrors and GenericErrs only intercept error responses (< 200 or >= 400). Success codes like
// 200, 204, 206 etc. pass through unmodified.
//
//   - Histogram: use NewPromHistogram to create an histogram used to capture prometheus metrics about every request
//     if left empty, no prometheus metric will be captured
type Middleware struct {
	jsonErrors   bool
	genericErrs  bool
	panicRecover bool
	hist         Histogram
	logger       *slog.Logger
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
					stack := debug.Stack()
					if c.logger != nil {
						c.logger.Error("panic recovered",
							slog.String("method", r.Method),
							slog.String("url", r.RequestURI),
							slog.String("panic", fmt.Sprint(rec)),
							slog.String("stack", string(stack)),
						)
					}
					respWriter.WriteHeader(http.StatusInternalServerError)
					_, _ = respWriter.Write([]byte(http.StatusText(http.StatusInternalServerError)))
				}
				c.finalize(w, r, respWriter, timeStart)
			}()
		}

		next.ServeHTTP(respWriter, r)

		if !c.panicRecover {
			c.finalize(w, r, respWriter, timeStart)
		}
	})
}

func (c *Middleware) finalize(w http.ResponseWriter, r *http.Request, respWriter *StatWriter, timeStart time.Time) {
	timeDiff := time.Since(timeStart)

	errMsg := c.getErrMsg(respWriter.statusCode, respWriter.buf)
	c.log(r, respWriter.StatusCode(), errMsg, timeDiff)

	if c.genericErrs {
		errMsg = http.StatusText(respWriter.StatusCode())
	}

	if IsStatusError(respWriter.statusCode) && !respWriter.BodyForwarded() {
		if c.jsonErrors {
			b := jsonErrBytes(errMsg, respWriter.StatusCode())
			w.Header().Set("Content-Type", "application/json")
			respWriter.flushHeader()
			_, _ = w.Write(b)
		} else {
			w.Header().Set("Content-Type", "text/plain")
			respWriter.flushHeader()
			_, _ = fmt.Fprint(w, errMsg)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
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
