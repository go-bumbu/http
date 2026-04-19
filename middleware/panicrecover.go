package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// PanicRecover returns a middleware that recovers from panics in downstream handlers,
// logs the panic with a stack trace, and returns a 500 response to the client.
func PanicRecover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := debug.Stack()
					if logger != nil {
						logger.Error("panic recovered",
							slog.String("method", r.Method),
							slog.String("url", r.RequestURI),
							slog.String("panic", fmt.Sprint(rec)),
							slog.String("stack", string(stack)),
						)
					}
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
