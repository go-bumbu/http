# AGENTS.md

This file provides guidance to AI coding agents (e.g. Claude Code) when working with code in this repository.

## Commands

```bash
make test              # run all tests with coverage
make lint              # run golangci-lint (must be installed externally)
make benchmark         # run benchmarks
make verify            # run tests + lint + benchmarks + coverage check
go test ./...          # run all tests
go test ./middleware/  # run tests for a single package
go test ./middleware/ -run TestMiddleware  # run a single test
```

## Architecture

This is a Go library (`github.com/go-bumbu/http`) providing reusable HTTP components for backend services. It is not an application — it's imported by other projects.

### Packages

- **middleware/** — Composable middleware chain using standard `func(next http.Handler) http.Handler` pattern. Stdlib-only: structured logging (slog) via the `Logger` interface, panic recovery, and a development delay. Metrics flow through the `Observer` interface.
- **middleware/metrics/** — Prometheus-backed `Observer` implementation, configured with a `metrics.Cfg`: `NewObserver` builds the registered request-duration histogram and wraps it as `middleware.Observer` (`NopObserver` is an explicit no-op; buckets are validated ascending at construction). The only package that imports `prometheus/client_golang`.
- **spa/** — Single Page Application handler serving files from an `fs.FS` (typically embedded), built with `spa.New(spa.Cfg{...})`. Blocks dotfiles (except `.well-known/`) and normalises `PathPrefix`.
- **outbound/** — Client-side resilience for a service's own outbound calls to third-party APIs: a throttled, retrying `Client` (built with `outbound.New(outbound.Cfg{...})`) with a typed `*Error` carrying a status, a user-facing message, and a `Kind`. The only package that imports `golang.org/x/time/rate`.
- **problemjson/** — RFC 9457 (`application/problem+json`) error-response `Writer`, built with `problemjson.New(problemjson.Cfg{...})`. `ModeMasked` strips detail in production; `WriteUpstream` bridges an `outbound.*Error` via the structural `UpstreamError` interface (no import) and, in dev mode, adds a `reason` field from an optional `UpstreamReason()` method.

### Key Design Decisions

- **StatWriter** (`middleware/respwriter.go`) wraps `http.ResponseWriter` to intercept the status code and the error-response body. On an error status it simultaneously buffers the body for logging and forwards (tees) it to the client — this prevents reverse-proxy hangs when the upstream writes an error body. The middleware never replaces the body; success and 1xx responses pass through untouched.
- **Streaming**: StatWriter implements `Flush`/`FlushError` and `Hijack` directly (not just `Unwrap`), so handlers using either `http.ResponseController` or the older `w.(http.Flusher)` / `w.(http.Hijacker)` type assertions can stream. The first flush calls `releaseInterception`, which commits the status code and switches to passthrough (error bodies are already teed as they are written, so nothing is buffered-but-unsent). `Streaming()` reports that the response has been streamed or hijacked and `Started()` reports that any bytes have been committed (any `Write` latches the status); panic recovery synthesises a 500 only when neither holds, so it never appends `Internal Server Error` over a body the client has already begun receiving — matching net/http. After a hijack, `flushHeader` writes nothing.
- **Error classification**: `IsStatusError()` (>= 400) vs `IsServerErr()` (>= 500) drives log levels — server errors log at ERROR, client errors at INFO. 1xx informational responses (e.g. 103 Early Hints) pass through without latching the status.
- **Panic recovery re-panics on `http.ErrAbortHandler`**: it's net/http's sentinel to abort a response so the client detects truncation (`ReverseProxy` uses it when the upstream dies mid-copy); swallowing it would make truncated responses look complete.
- **Bounded Prometheus label cardinality** (in `middleware/metrics`): the `addr` label uses `r.Pattern` (route pattern, requires Go 1.23 for the `r.Pattern` field) when matched, else the constant `"unmatched"` — never the raw request path. The `method` label is normalised to the standard HTTP methods, else `"other"`. Both cap the label sets so an attacker cannot mint unbounded time-series (net/http accepts any token as a method, and a non-pattern mux leaves `r.Pattern` empty).
- **Backend-agnostic seams**: the combined middleware depends on the `Observer` (metrics) and `Logger` (logging) interfaces, not concrete types. `*slog.Logger` satisfies `Logger` structurally; `metrics.NewObserver` supplies `Observer`. This keeps `middleware` free of any third-party dependency — Prometheus is compiled only when a consumer imports `middleware/metrics`.
- **Bounded log buffer**: error-response bodies are captured for logging in an unexported `middleware.limitBuf` (cap `bufMaxBytes` = 2000) that *composes* (not embeds) `bytes.Buffer` and behaves like a capped `io.Discard` — writes past the cap are dropped and flagged via `Truncated()`, so a large error body cannot grow middleware memory without bound. Composition (not embedding) is deliberate: it prevents a promoted `bytes.Buffer` method (`WriteString`, `ReadFrom`, …) from bypassing the cap.
- **Constructor convention**: configurable constructors take a single `Cfg` struct — `middleware.New`, `outbound.New`, `problemjson.New`, `spa.New`, and `metrics.NewObserver` — so adjacent same-typed arguments can't be transposed and options can grow without breaking callers. The low-level `middleware.NewWriter(w)` is the deliberate exception (a single wrapper argument, like `bufio.NewWriter`).
- **spa dotfile blocking & prefix normalisation** (`spa/spa.go`): `http.FileServerFS` serves dotfiles, so `spa.Handler` returns 404 for any path segment beginning with `.` — except the standard `.well-known/` tree — so `/.env` or `/.git/config` can't leak over an `os.DirFS` build directory. A plain `//go:embed dir` already excludes dotfiles from the binary; the serve-time block covers `os.DirFS` and `all:` embeds. `PathPrefix` is normalised in the constructor (`ui`, `/ui`, `/ui/` → `/ui`), so a missing leading slash can't silently route every asset to `index.html`.
- **Upstream-failure presentation seam**: `outbound` owns the failure taxonomy (`Kind` → HTTP status); `problemjson` owns presentation (status → slug/title). The HTTP status is the deliberate contract between them, so several `Kind`s collapse to one 502 slug by design — the finer `Kind` stays in server logs, and in `problemjson` dev mode it also appears in the `reason` field (read via an optional `UpstreamReason()` method). Neither package imports the other.

## Linting

Uses golangci-lint v2 with: nolintlint, gocyclo (max 20), nestif (max 5), gosec, dupl. All `//nolint` directives require an explanation and specific linter name.
