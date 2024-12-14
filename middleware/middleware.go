package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Cfg struct {
	JsonErrors  bool
	GenericErrs bool // print generic error messages instead of the actual one
	Logger      *slog.Logger
	Histogram   Histogram
}

func New(cfg Cfg) *Middleware {
	m := Middleware{
		jsonErrors:  cfg.JsonErrors,
		genericErrs: cfg.GenericErrs,
		hist:        cfg.Histogram,
		logger:      cfg.Logger,
	}
	return &m
}

// Middleware is intended perform common actions done by a production http server, it has several configuration flags:
//   - JsonErrors: if set to true it will intercept all responses != 200, read the response error handlerMsg and
//     wrap it into a json file, this is useful for APIs
//   - GenericErrs: if set to true the error handlerMsg responded to the en user is a generic handlerMsg based on the
//     response code instead of the original error handlerMsg, the original error will still be logged.
//
// NOTE: both JsonErrors and GenericErrs assumes that only 200 response codes contain usable body, e.g. don't
// return html in case of errors
//
//   - Histogram: use NewPromHistogram to create an histogram used to capture prometheus metrics about every request
//     if left empty, no prometheus metric will be captured
type Middleware struct {
	jsonErrors  bool
	genericErrs bool
	hist        Histogram
	logger      *slog.Logger
}

// Middleware is an HTTP middleware that checks the Config and applies logic based on it.
func (c *Middleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeStart := time.Now()
		respWriter := NewWriter(w, true)

		next.ServeHTTP(respWriter, r)
		timeDiff := time.Since(timeStart)

		// get the generic or the specific error string
		errMsg := c.getErrMsg(respWriter.statusCode, respWriter.buf)
		c.log(r, respWriter.StatusCode(), errMsg, timeDiff)

		if c.genericErrs {
			errMsg = http.StatusText(respWriter.StatusCode())
		}

		if respWriter.statusCode != 200 {
			if c.jsonErrors {
				b := jsonErrBytes(errMsg, respWriter.StatusCode())
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, string(b))
			} else {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = fmt.Fprint(w, errMsg)
			}
		}

		c.observe(r, respWriter.StatusCode(), timeDiff)
	})
}

// getErrMsg returns the error handlerMsg in case of a request != 200 or empty string
func (c *Middleware) getErrMsg(code int, buf io.Reader) string {
	if code == 200 {
		return ""
	}

	msgB, err := io.ReadAll(buf)
	if err != nil && c.logger != nil {
		// I wish I could estimate the conditions of this error
		c.logger.Error("error while reading buffer error handlerMsg:", slog.Any("err", err))
	}
	return strings.Trim(string(msgB), "\n")
}

type jsonErr struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

func jsonErrBytes(error string, code int) []byte {
	if code == 0 {
		code = http.StatusInternalServerError
	}
	payload := jsonErr{
		Error: error,
		Code:  code,
	}
	byteErr, _ := json.Marshal(payload)
	return byteErr
}
