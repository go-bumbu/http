# Http

Reusable HTTP building blocks for Go backend services — deliberately spanning both
**inbound** request plumbing (middleware, metrics, SPA serving, RFC 9457 errors) and
**outbound** calls to third-party APIs (a throttled, retrying client).

The packages are independent: each stands alone, and you only compile the dependencies
of the packages you actually import — Prometheus comes in only with `middleware/metrics`,
`golang.org/x/time/rate` only with `outbound`.

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

obs, err := metrics.NewObserver(metrics.Cfg{Prefix: "requests"})
// handle err

m := middleware.New(middleware.Cfg{
    PanicRecover: true,
    Logger:       slog.Default(),
    Metrics:      obs,
})
mux.Handle("/", m.Wrap(handler))
```

It depends only on the standard library; Prometheus is pulled in only when you import `middleware/metrics`.

`ReqDelay` is a standalone, development-only helper: `middleware.ReqDelay{...}.Delay` adds a random delay between a min and max duration to simulate slow backends.

#### middleware/metrics

Prometheus-backed metrics: `NewObserver` builds a registered request-duration
histogram and wraps it as a `middleware.Observer` for `middleware.Cfg.Metrics`
(`NopObserver` returns an explicit no-op). Request labels are bounded to closed sets
to avoid time-series cardinality blow-ups. This is the only package that imports the
Prometheus client, so importing `middleware` alone stays dependency-free.

### spa

Single Page Application handler that serves files from an `fs.FS` (typically `embed.FS`).
Requests for unknown paths fall back to `index.html`, allowing client-side routing.

```go
spaHandler, err := spa.New(spa.Cfg{FS: embeddedFS, SubDir: "dist", PathPrefix: "/ui"})
```

`spa.Cfg`:
- `FS` — the filesystem holding the SPA assets (required)
- `SubDir` — sub-directory within the FS to serve from (empty for the FS root)
- `PathPrefix` — URL path the SPA is mounted under; normalised to a single leading slash, so `ui`, `/ui`, and `/ui/` are equivalent (empty mounts at the root)

Dotfiles are not served — `GET /.env` or `/.git/config` returns 404 — except the standard
`.well-known/` tree. A plain `//go:embed dist` already keeps dotfiles out of the binary
(prefer it to `//go:embed all:dist`); the serve-time block is the defense for `os.DirFS`
and `all:` embeds.

### outbound

A throttled, retrying HTTP client for a service's own outbound calls to third-party
APIs, so every such client handles a flaky provider the same way: fair-use rate
limiting, bounded exponential-backoff retries for transient failures (5xx, 429,
timeouts), `Retry-After` compliance, and a typed `*Error` carrying both the technical
detail (for logs) and a human-readable sentence naming the service (for the UI).

```go
client := outbound.New(outbound.Cfg{Service: "Cover Art Archive", RPS: 5})
resp, err := client.Get(ctx, url, nil)
// on failure: outbound.HTTPStatus(err) and outbound.UserMessage(err, fallback)
```

This is the only package that pulls in `golang.org/x/time/rate`; the inbound packages above do not.

### problemjson

Writes RFC 9457 (`application/problem+json`) error responses — one consistent,
spec-shaped error body (a stable `type` URI, `title`/`detail`, and the request path
as `instance`) in place of ad-hoc `{"error":...}` shapes. `ModeMasked` strips
handler-supplied detail in production while keeping a correlation `reference`.

```go
pj, err := problemjson.New(problemjson.Cfg{BaseURI: "https://errors.example.com"})
// handle err
pj.Write(w, r, http.StatusNotFound, problemjson.SlugNotFound, "no such release")
pj.WriteUpstream(w, r, err, "The catalog is unavailable.") // maps an outbound.*Error
```

In dev mode, `WriteUpstream` adds an extra `reason` field naming the exact failure
(e.g. `unreachable`) whenever the error can supply one through an optional
`UpstreamReason() string` method (`outbound.*Error` does). `ModeMasked` strips it, and
neither package imports the other — the reason travels through the method name alone.
