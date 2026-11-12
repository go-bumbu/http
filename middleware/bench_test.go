package middleware_test

import (
	"bytes"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/go-bumbu/http/middleware"
)

// Resource budget: TestWrapResourceBudget fails when a case costs more than its recorded
// baseline times these factors. They are deliberately loose — the guard is for a change that
// blows the cost up, not for a few percent — and time gets far more room than memory because
// it depends on the machine (CI runners, coverage instrumentation) while allocations do not.
const (
	memBudgetFactor  = 2.0  // allocs/op and B/op
	timeBudgetFactor = 10.0 // ns/op
)

// wrapBenchCase is one request path through Middleware.Wrap. The baseline fields are the cost
// measured when the case was last tuned (go1.27, amd64, no race detector); after an intended
// cost change, re-measure with `go test -run=^$ -bench=BenchmarkWrap -benchmem ./middleware/`
// and update them.
type wrapBenchCase struct {
	name    string
	cfg     middleware.Cfg
	handler http.Handler

	baseNs, baseBytes, baseAllocs float64
}

func wrapBenchCases() []wrapBenchCase {
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", testHandler(http.StatusOK, "ok"))

	info := slog.New(slog.NewJSONHandler(io.Discard, nil))
	debug := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// A body far larger than the capped log buffer: it is teed to the client in full while
	// only bufMaxBytes are captured, so this case catches the cap being lost.
	largeBody := bytes.Repeat([]byte("x"), 4096)
	largeErr := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		for range 16 { // 64 KiB
			_, _ = w.Write(largeBody)
		}
	})
	panics := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	chunk := []byte("data: chunk\n\n")
	stream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		for range 8 {
			_, _ = w.Write(chunk)
			_ = rc.Flush()
		}
	})

	return []wrapBenchCase{
		{
			name: "no metrics", cfg: middleware.Cfg{}, handler: mux,
			baseNs: 210, baseBytes: 128, baseAllocs: 3,
		},
		{
			name: "metrics", cfg: middleware.Cfg{Metrics: nopObserver{}}, handler: mux,
			baseNs: 430, baseBytes: 616, baseAllocs: 8,
		},
		{
			name: "metrics+groups", cfg: middleware.Cfg{Metrics: nopObserver{}, RouteGroups: []string{"/users"}}, handler: mux,
			baseNs: 430, baseBytes: 632, baseAllocs: 9,
		},
		{
			name: "metrics+CaptureRoute", cfg: middleware.Cfg{Metrics: nopObserver{}}, handler: middleware.CaptureRoute(mux),
			baseNs: 660, baseBytes: 1017, baseAllocs: 12,
		},
		{
			name: "logger", cfg: middleware.Cfg{Logger: info}, handler: mux,
			baseNs: 1300, baseBytes: 433, baseAllocs: 6,
		},
		{
			name: "logger+error body", cfg: middleware.Cfg{Logger: info}, handler: testHandler(http.StatusInternalServerError, "db connection broken"),
			baseNs: 1400, baseBytes: 1018, baseAllocs: 8,
		},
		{
			name: "logger+large error body", cfg: middleware.Cfg{Logger: info}, handler: largeErr,
			baseNs: 4500, baseBytes: 7085, baseAllocs: 9,
		},
		{
			name: "logger+debug headers", cfg: middleware.Cfg{Logger: debug, LogHeaders: true}, handler: mux,
			baseNs: 2600, baseBytes: 1122, baseAllocs: 14,
		},
		{
			name: "logger+panic recovered", cfg: middleware.Cfg{Logger: info, PanicRecover: true}, handler: panics,
			baseNs: 17000, baseBytes: 6111, baseAllocs: 15,
		},
		{
			name: "logger+streaming", cfg: middleware.Cfg{Logger: info}, handler: stream,
			baseNs: 1200, baseBytes: 416, baseAllocs: 5,
		},
		{
			name: "full", cfg: middleware.Cfg{
				Logger: info, Metrics: nopObserver{}, PanicRecover: true, RouteGroups: []string{"/users"},
			}, handler: middleware.CaptureRoute(mux),
			baseNs: 1950, baseBytes: 1339, baseAllocs: 16,
		},
	}
}

// benchRequest is the request every case serves; the headers feed the debug header log
// (Authorization exercises redaction).
func benchRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/users/1", nil)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("User-Agent", "bench")
	r.Header.Set("Request-Id", "req-1")
	return r
}

// discardWriter drops the response body, so the numbers are the middleware's own cost and not
// a recorder buffering every iteration's response. It supports Flush for the streaming case.
type discardWriter struct{ h http.Header }

func newDiscardWriter() *discardWriter { return &discardWriter{h: http.Header{}} }

func (w *discardWriter) Header() http.Header         { return w.h }
func (w *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *discardWriter) WriteHeader(int)             {}
func (w *discardWriter) Flush()                      {}

// BenchmarkWrap measures the per-request cost of Middleware.Wrap on each path: with and
// without metrics (the route slot and request copy are only added with metrics), CaptureRoute's
// extra request copy, access logging, error-body capture, debug header logging, panic
// recovery, and streaming.
func BenchmarkWrap(b *testing.B) {
	for _, bc := range wrapBenchCases() {
		b.Run(bc.name, func(b *testing.B) {
			h := middleware.New(bc.cfg).Wrap(bc.handler)
			r := benchRequest()
			w := newDiscardWriter()
			b.ReportAllocs()
			for b.Loop() {
				h.ServeHTTP(w, r)
			}
		})
	}
}

// TestWrapResourceBudget guards against a change that makes the middleware dramatically more
// expensive: each BenchmarkWrap case must stay within its baseline times memBudgetFactor
// (allocs/op, B/op) and timeBudgetFactor (ns/op). Unlike the benchmark it runs with go test,
// so CI enforces it.
func TestWrapResourceBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("resource budget measurement skipped in -short mode")
	}
	if raceEnabled {
		// The race detector slows everything down and makes sync.Pool drop items at random,
		// so neither time nor allocations are comparable to the baseline.
		t.Skip("resource budget measurement skipped under the race detector")
	}
	for _, bc := range wrapBenchCases() {
		t.Run(bc.name, func(t *testing.T) {
			h := middleware.New(bc.cfg).Wrap(bc.handler)
			got := measure(h, benchRequest(), 5, 2000)
			t.Logf("%.0f ns/op, %.0f B/op, %.0f allocs/op", got.ns, got.bytes, got.allocs)

			check := func(metric string, got, base, factor float64) {
				t.Helper()
				if limit := base * factor; got > limit {
					t.Errorf("%s = %.0f, over budget %.0f (baseline %.0f × %.0f); if the increase is intended, "+
						"update the baseline in wrapBenchCases", metric, got, limit, base, factor)
				}
			}
			check("allocs/op", got.allocs, bc.baseAllocs, memBudgetFactor)
			check("B/op", got.bytes, bc.baseBytes, memBudgetFactor)
			check("ns/op", got.ns, bc.baseNs, timeBudgetFactor)
		})
	}
}

type cost struct{ ns, bytes, allocs float64 }

// measure serves r through h in rounds of n requests and returns the per-request cost of the
// cheapest round for each metric, counted the way testing.B does (runtime.MemStats) but in a
// few milliseconds instead of a second per case. Taking the minimum over rounds discards
// rounds disturbed by GC or the scheduler.
func measure(h http.Handler, r *http.Request, rounds, n int) cost {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	w := newDiscardWriter()
	h.ServeHTTP(w, r) // warm up lazily initialised state and pools

	best := cost{ns: math.Inf(1), bytes: math.Inf(1), allocs: math.Inf(1)}
	var before, after runtime.MemStats
	for range rounds {
		runtime.ReadMemStats(&before)
		start := time.Now()
		for range n {
			h.ServeHTTP(w, r)
		}
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)

		best.ns = min(best.ns, float64(elapsed.Nanoseconds())/float64(n))
		best.bytes = min(best.bytes, float64(after.TotalAlloc-before.TotalAlloc)/float64(n))
		best.allocs = min(best.allocs, float64(after.Mallocs-before.Mallocs)/float64(n))
	}
	return best
}
