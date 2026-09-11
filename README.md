# Http

Reusable HTTP packages for Go backend services.

## Install

```
go get github.com/go-bumbu/http
```

## Packages

### middleware

Composable HTTP middleware using the standard `func(next http.Handler) http.Handler` pattern.
The combined `Middleware` struct is the primary entry point — it orchestrates logging, metrics,
and panic recovery in a single handler:

```go
import (
    "log/slog"

    "github.com/go-bumbu/http/middleware"
    "github.com/go-bumbu/http/middleware/metrics"
)

hist, err := metrics.NewPromHistogram("requests", nil, nil)
// handle err

m := middleware.New(middleware.Cfg{
    PanicRecover: true,
    Logger:       slog.Default(),
    Metrics:      metrics.NewObserver(hist),
})
mux.Handle("/", m.Middleware(handler))
```

It depends only on the standard library; Prometheus is pulled in only when you import `middleware/metrics`.

`ReqDelay` is a standalone, development-only helper: `middleware.ReqDelay{...}.Delay` adds a random delay between a min and max duration to simulate slow backends.

#### middleware/metrics

Prometheus-backed metrics: `NewPromHistogram` builds a registered request-duration
histogram, and `NewObserver` adapts a histogram to `middleware.Observer` for
`middleware.Cfg.Metrics`. This is the only package that imports the Prometheus client,
so importing `middleware` alone stays dependency-free.

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
