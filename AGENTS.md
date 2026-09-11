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
- **middleware/metrics/** — Prometheus-backed `Observer` implementation (`NewObserver`), the request-duration histogram (`NewPromHistogram`), and a standalone metrics `Middleware`. The only package that imports `prometheus/client_golang`.
- **spa/** — Single Page Application handler serving files from an `fs.FS` (typically embedded).

### Key Design Decisions

- **StatWriter** (`middleware/respwriter.go`) wraps `http.ResponseWriter` to intercept the status code and the error-response body. On an error status it simultaneously buffers the body for logging and forwards (tees) it to the client — this prevents reverse-proxy hangs when the upstream writes an error body. The middleware never replaces the body; success and 1xx responses pass through untouched.
- **Streaming**: StatWriter implements `Flush`/`FlushError` and `Hijack` directly (not just `Unwrap`), so handlers using either `http.ResponseController` or the older `w.(http.Flusher)` / `w.(http.Hijacker)` type assertions can stream. The first flush calls `releaseInterception`, which commits the status code and switches to passthrough (error bodies are already teed as they are written, so nothing is buffered-but-unsent). `Streaming()` reports that the response has been streamed or hijacked, so panic recovery will not synthesise a 500 over bytes the client already has; after a hijack, `flushHeader` writes nothing.
- **Error classification**: `IsStatusError()` (>= 400) vs `IsServerErr()` (>= 500) drives log levels — server errors log at ERROR, client errors at INFO. 1xx informational responses (e.g. 103 Early Hints) pass through without latching the status.
- **Panic recovery re-panics on `http.ErrAbortHandler`**: it's net/http's sentinel to abort a response so the client detects truncation (`ReverseProxy` uses it when the upstream dies mid-copy); swallowing it would make truncated responses look complete.
- **Prometheus `addr` label** (in `middleware/metrics`, `metricAddr`) uses `r.Pattern` (route pattern, requires Go 1.23 `ServeMux`) when available, raw path as fallback — avoids per-URL time-series cardinality explosion.
- **Backend-agnostic seams**: the combined middleware depends on the `Observer` (metrics) and `Logger` (logging) interfaces, not concrete types. `*slog.Logger` satisfies `Logger` structurally; `metrics.NewObserver` supplies `Observer`. This keeps `middleware` free of any third-party dependency — Prometheus is compiled only when a consumer imports `middleware/metrics`.
- **Bounded log buffer**: error-response bodies are captured for logging in an unexported `middleware.limitBuf` (cap `bufMaxBytes` = 2000) that *composes* (not embeds) `bytes.Buffer` and behaves like a capped `io.Discard` — writes past the cap are dropped and flagged via `Truncated()`, so a large error body cannot grow middleware memory without bound. Composition (not embedding) is deliberate: it prevents a promoted `bytes.Buffer` method (`WriteString`, `ReadFrom`, …) from bypassing the cap.

## Linting

Uses golangci-lint v2 with: nolintlint, gocyclo (max 20), nestif (max 5), gosec, dupl. All `//nolint` directives require an explanation and specific linter name.
