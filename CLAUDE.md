# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

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

- **middleware/** — Composable middleware chain using standard `func(next http.Handler) http.Handler` pattern. Includes: structured logging (slog), Prometheus metrics, JSON error wrapping, generic error messages, and development delay.
- **handlers/spa/** — Single Page Application handler serving files from an `fs.FS` (typically embedded).
- **lib/limitio/** — Internal IO utilities: bounded buffer (2000 byte cap) and limited writer.

### Key Design Decisions

- **StatWriter** (`middleware/respwriter.go`) wraps `http.ResponseWriter` to intercept status codes and error bodies. The `teeOnErr` flag simultaneously buffers the body for logging while forwarding it to the client — this prevents reverse-proxy hangs when the upstream writes an error body.
- **Streaming**: StatWriter implements `Flush`/`FlushError` and `Hijack` directly (not just `Unwrap`), so handlers using either `http.ResponseController` or the older `w.(http.Flusher)` / `w.(http.Hijacker)` type assertions can stream. The first flush calls `releaseInterception`, which commits the real (possibly deferred) status code, forwards anything already buffered, and switches to passthrough — so an error-status stream is not collapsed into a single replaced body. `Streaming()` and `canReplaceBody()` gate whether the middleware may still write a replacement body; after a hijack, `flushHeader` writes nothing.
- **Error classification**: `IsStatusError()` (>= 400) vs `IsServerErr()` (>= 500) drives log levels — server errors log at ERROR, client errors at INFO. 1xx informational responses (e.g. 103 Early Hints) pass through without latching the status.
- **Body replacement fixes headers**: when `JsonErrors`/`GenericErrs` replace an error body, `writeReplacementBody` (`middleware/errors.go`) resets `Content-Length` and removes `Content-Encoding` — a stale length from the handler (or a proxied upstream) makes clients fail the read with "unexpected EOF".
- **Panic recovery re-panics on `http.ErrAbortHandler`**: it's net/http's sentinel to abort a response so the client detects truncation (`ReverseProxy` uses it when the upstream dies mid-copy); swallowing it would make truncated responses look complete.
- **Prometheus `addr` label** uses `r.Pattern` (route pattern, requires Go 1.23 `ServeMux`) when available, raw path as fallback — avoids per-URL time-series cardinality explosion.

## Linting

Uses golangci-lint v2 with: nolintlint, gocyclo (max 20), nestif (max 5), gosec, dupl. All `//nolint` directives require an explanation and specific linter name.
