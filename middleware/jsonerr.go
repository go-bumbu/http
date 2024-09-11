package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type jsonErr struct {
	Error string
	Code  int
}

func jsonError(w http.ResponseWriter, error string, code int) {
	if code == 0 {
		code = http.StatusInternalServerError
	}
	payload := jsonErr{
		Error: error,
		Code:  code,
	}
	byteErr, _ := json.Marshal(payload)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprint(w, string(byteErr))
}

// JsonErrMiddleware is a middleware mostly intended to handle http errors on a JSON api
// it will check if a handler wrote a non 2xx response code, intercept the response and
// transform it to a generic JSON error response.
//
// the motivation is that we don't always control the error format of our responses and maybe
// also don't want to force json error responses always
//
// if genericMessage is set to true, the error message will not printed; only the generic
// message related to the response code; this is intended for production environments
// to prevent leaking details about errors.
// TODO: this assumes that only 200 response codes contain usable body, e.g. not usable for rendering html
func JsonErrMiddleware(next http.Handler, genericMessage bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respWriter := NewWriter(w, true)
		next.ServeHTTP(respWriter, r)
		code := respWriter.StatusCode()
		msg := http.StatusText(code)

		if !genericMessage {
			msgB, err := io.ReadAll(respWriter.buf)
			if err != nil {
				jsonError(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
			msg = string(msgB)
			msg = strings.Trim(msg, "\n")
		}
		if IsStatusError(code) {
			jsonError(w, msg, code)
		}
	})
}
