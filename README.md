# Http

Reusable HTTP packages for Go backend services.

## Install

```
go get github.com/go-bumbu/http
```

## Packages

### middleware

Composable HTTP middleware using the standard `func(next http.Handler) http.Handler` pattern.
Middleware can be used individually or combined via the `Middleware` struct which orchestrates all features in a single handler.

**Standalone middleware:**

| Middleware | Import | Description |
|---|---|---|
| `Logging` | `middleware.Logging(logger)` | Structured request logging via `log/slog`. Logs at INFO for client errors, ERROR for server errors. Captures error response bodies. |
| `Metrics` | `middleware.Metrics(hist)` | Prometheus histogram recording request duration, method, path, status code, and error flag. |
| `JSONErrors` | `middleware.JSONErrors(generic)` | Intercepts error responses (>= 400) and wraps the body in `{"error":"...","code":N}`. Optionally replaces messages with generic status text. |
| `GenericErrors` | `middleware.GenericErrors()` | Replaces error response bodies with the standard status text (e.g. "Internal Server Error"). |
| `PanicRecover` | `middleware.PanicRecover(logger)` | Recovers from panics, logs a stack trace, and returns 500 to the client. |
| `ReqDelay` | `middleware.ReqDelay{...}.Delay` | Adds a random delay between min/max duration. Useful during development to simulate slow backends. |

**Combined middleware:**

```go
m := middleware.New(middleware.Cfg{
    JsonErrors:   true,
    GenericErrs:  true,
    PanicRecover: true,
    Logger:       slog.Default(),
    PromHisto:    hist,
})
mux.Handle("/", m.Middleware(handler))
```

The combined `Middleware` struct runs logging, metrics, error wrapping, and panic recovery in a single pass.

### handlers/spa

Single Page Application handler that serves files from an `fs.FS` (typically `embed.FS`).
Requests for unknown paths fall back to `index.html`, allowing client-side routing.

```go
spaHandler, err := handlers.NewSpaHAndler(embeddedFS, "dist", "/ui")
```

Parameters:
- `inputFs` — the filesystem containing the SPA assets
- `fsSubDir` — subdirectory within the FS to serve from (empty string for root)
- `pathPrefix` — URL path prefix where the SPA is mounted

### lib/limitio

Internal IO utilities for bounded writes.

- **`LimitedBuf`** — A `bytes.Buffer` that stops accepting data after a configured byte limit (default 2000 in the middleware). Returns `ErrBufferLimit` when the cap is reached. Used to safely buffer error response bodies for logging without unbounded memory growth.
- **`LimitWriter`** — Wraps any `io.Writer` and caps total bytes written, returning `io.EOF` at the limit.
