package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// newTestClient returns a Client pointed at srv with throttling disabled and
// waits recorded instead of slept, so retry timing is asserted without
// wall-clock delay.
func newTestClient(t *testing.T, srv *httptest.Server) (*Client, *[]time.Duration) {
	t.Helper()
	var waits []time.Duration
	cfg := Cfg{
		Service:   "Example Service",
		UserAgent: "example-client/1.0",
		Wait: func(_ context.Context, dur time.Duration) error {
			waits = append(waits, dur)
			return nil
		},
	}
	if srv != nil {
		cfg.Client = srv.Client()
	}
	return New(cfg), &waits
}

func TestGetRetriesServerErrorThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, waits := newTestClient(t, srv)
	resp, err := c.Get(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("want 2 attempts, got %d", n)
	}
	if len(*waits) != 1 {
		t.Fatalf("want one backoff wait, got %v", *waits)
	}
}

func TestGetGivesUpAfterMaxAttempts(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	_, err := c.Get(context.Background(), srv.URL, nil)
	var uerr *Error
	if !errors.As(err, &uerr) {
		t.Fatalf("want *upstream.Error, got %T: %v", err, err)
	}
	if uerr.Kind != KindUnavailable || uerr.Status != http.StatusInternalServerError {
		t.Fatalf("unexpected error: kind=%v status=%d", uerr.Kind, uerr.Status)
	}
	if n := atomic.LoadInt32(&hits); int(n) != c.maxAttempts {
		t.Fatalf("want %d attempts, got %d", c.maxAttempts, n)
	}
	if got := HTTPStatus(err); got != http.StatusBadGateway {
		t.Fatalf("HTTPStatus = %d, want 502", got)
	}
	msg := uerr.UserMessage()
	if !strings.Contains(msg, "Example Service") || !strings.Contains(msg, "unavailable") {
		t.Fatalf("unhelpful user message: %q", msg)
	}
	if strings.Contains(msg, "status 500") {
		t.Fatalf("user message leaks internal wording: %q", msg)
	}
}

func TestGetHonoursRetryAfterOnRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, waits := newTestClient(t, srv)
	_, err := c.Get(context.Background(), srv.URL, nil)
	var uerr *Error
	if !errors.As(err, &uerr) {
		t.Fatalf("want *upstream.Error, got %T: %v", err, err)
	}
	if uerr.Kind != KindRateLimited {
		t.Fatalf("kind = %v, want rate limited", uerr.Kind)
	}
	if got := HTTPStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus = %d, want 429", got)
	}
	for _, w := range *waits {
		if w != 2*time.Second {
			t.Fatalf("want Retry-After honoured (2s waits), got %v", *waits)
		}
	}
	if msg := uerr.UserMessage(); !strings.Contains(msg, "too many requests") {
		t.Fatalf("unhelpful rate-limit message: %q", msg)
	}
}

func TestGetReturnsAllowedStatusWithoutRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	resp, err := c.Get(context.Background(), srv.URL, nil, http.StatusNotFound)
	if err != nil {
		t.Fatalf("an allowed status must not be an error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("an allowed status must not be retried, got %d attempts", n)
	}
}

func TestGetDoesNotRetryRejectedRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	_, err := c.Get(context.Background(), srv.URL, nil)
	var uerr *Error
	if !errors.As(err, &uerr) {
		t.Fatalf("want *upstream.Error, got %T: %v", err, err)
	}
	if uerr.Kind != KindRejected {
		t.Fatalf("kind = %v, want rejected", uerr.Kind)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("a 4xx must not be retried, got %d attempts", n)
	}
}

func TestGetTransportFailureIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	c, _ := newTestClient(t, nil)
	_, err := c.Get(context.Background(), url, nil)
	var uerr *Error
	if !errors.As(err, &uerr) {
		t.Fatalf("want *upstream.Error, got %T: %v", err, err)
	}
	if uerr.Kind != KindUnreachable {
		t.Fatalf("kind = %v, want unreachable", uerr.Kind)
	}
	if msg := uerr.UserMessage(); !strings.Contains(msg, "could not be reached") {
		t.Fatalf("unhelpful message: %q", msg)
	}
}

func TestGetThrottleGatesRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	// A limiter with burst 0 can never admit a request, so Wait always fails.
	c := New(Cfg{
		Service: "Example Service",
		Client:  srv.Client(),
		Limiter: rate.NewLimiter(1, 0),
	})
	if _, err := c.Get(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("expected the throttle to block the request")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("request reached the server despite the throttle: %d hits", n)
	}
}

func TestGetSendsUserAgentAndHeaders(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv)
	resp, err := c.Get(context.Background(), srv.URL, http.Header{"Accept": []string{"application/json"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()
	if gotUA != "example-client/1.0" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestGetUnthrottledWhenRPSZero(t *testing.T) {
	c := New(Cfg{Service: "Example Service"})
	if c.limiter != nil {
		t.Fatal("RPS <= 0 must leave the client unthrottled (nil limiter)")
	}
}

// IsRejected lets a caller treat "the provider refused this identifier" as an
// empty result while still propagating real outages.
func TestIsRejectedOnlyMatchesRefusals(t *testing.T) {
	rejected := &Error{Service: "Example Service", Kind: KindRejected, Status: http.StatusNotFound}
	if !IsRejected(rejected) {
		t.Error("a 4xx refusal should be reported as rejected")
	}
	for _, kind := range []Kind{KindUnavailable, KindRateLimited, KindTimeout, KindUnreachable} {
		if IsRejected(&Error{Kind: kind}) {
			t.Errorf("kind %v must not be treated as a refusal", kind)
		}
	}
	if IsRejected(errors.New("boom")) {
		t.Error("an untyped error must not be treated as a refusal")
	}
	if IsRejected(nil) {
		t.Error("nil must not be treated as a refusal")
	}
}

func TestUserMessageFallsBackForPlainErrors(t *testing.T) {
	if got := UserMessage(errors.New("boom"), "Could not load data."); got != "Could not load data." {
		t.Fatalf("UserMessage = %q, want the fallback", got)
	}
	uerr := &Error{Service: "Example Service", Kind: KindTimeout}
	if got := UserMessage(uerr, "fallback"); got != uerr.UserMessage() {
		t.Fatalf("UserMessage = %q, want the typed message", got)
	}
	if got := HTTPStatus(errors.New("boom")); got != http.StatusBadGateway {
		t.Fatalf("HTTPStatus = %d, want 502 for untyped errors", got)
	}
	if got := HTTPStatus(uerr); got != http.StatusGatewayTimeout {
		t.Fatalf("HTTPStatus = %d, want 504 for a timeout", got)
	}
}

func TestKindString(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindUnavailable: "unavailable",
		KindRateLimited: "rate_limited",
		KindTimeout:     "timeout",
		KindUnreachable: "unreachable",
		KindRejected:    "rejected",
		KindBadResponse: "bad_response",
		Kind(99):        "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}
