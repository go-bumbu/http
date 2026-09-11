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
| `PanicRecover` | `middleware.PanicRecover(logger)` | Recovers from panics, logs a stack trace, and returns 500 to the client. |
| `ReqDelay` | `middleware.ReqDelay{...}.Delay` | Adds a random delay between min/max duration. Useful during development to simulate slow backends. |

**Combined middleware:**

```go
m := middleware.New(middleware.Cfg{
    PanicRecover: true,
    Logger:       slog.Default(),
    PromHisto:    hist,
})
mux.Handle("/", m.Middleware(handler))
```

The combined `Middleware` struct runs logging, metrics, generic error handling, and panic recovery in a single pass.

### spa

Single Page Application handler that serves files from an `fs.FS` (typically `embed.FS`).
Requests for unknown paths fall back to `index.html`, allowing client-side routing.

```go
spaHandler, err := spa.NewHandler(embeddedFS, "dist", "/ui")
```

Parameters:
- `inputFs` — the filesystem containing the SPA assets
- `fsSubDir` — subdirectory within the FS to serve from (empty string for root)
- `pathPrefix` — URL path prefix where the SPA is mounted
