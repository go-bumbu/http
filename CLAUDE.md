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
- **Error classification**: `IsStatusError()` (< 200 or >= 400) vs `IsServerErr()` (>= 500) drives log levels — server errors log at ERROR, client errors at INFO.

## Linting

Uses golangci-lint v2 with: nolintlint, gocyclo (max 20), nestif (max 5), gosec, dupl. All `//nolint` directives require an explanation and specific linter name.
