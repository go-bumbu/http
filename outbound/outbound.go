// Package outbound is shared HTTP plumbing for a service's outbound calls to
// third-party HTTP APIs.
//
// It exists so every such client handles a flaky provider the same way:
// fair-use throttling, a bounded retry with exponential backoff for transient
// failures (5xx, 429, timeouts), Retry-After compliance, and a typed *Error
// that carries both the technical detail (logs) and a human-readable sentence
// naming the service (the UI). Callers map the error to a status code with
// HTTPStatus and a message with UserMessage, so an upstream hiccup never
// surfaces as a raw "status 500" to the user.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// Kind classifies an upstream failure. It decides both the status code the
// caller answers with and the sentence the user reads.
type Kind int

const (
	// KindUnavailable is a persistent upstream server error (5xx) that
	// outlived the retries.
	KindUnavailable Kind = iota
	// KindRateLimited is a 429: we are being throttled by the provider.
	KindRateLimited
	// KindTimeout is a request that did not complete in time.
	KindTimeout
	// KindUnreachable is a transport-level failure (DNS, refused, reset).
	KindUnreachable
	// KindRejected is a 4xx other than 429 — the request itself was refused,
	// so retrying is pointless.
	KindRejected
	// KindBadResponse is a 2xx whose body could not be parsed.
	KindBadResponse
)

// String returns a short, greppable label for k, for logs and error text.
func (k Kind) String() string {
	switch k {
	case KindUnavailable:
		return "unavailable"
	case KindRateLimited:
		return "rate_limited"
	case KindTimeout:
		return "timeout"
	case KindUnreachable:
		return "unreachable"
	case KindRejected:
		return "rejected"
	case KindBadResponse:
		return "bad_response"
	default:
		return "unknown"
	}
}

// defaults for a new Client. Three attempts over ~1.5s of backoff keeps a
// user-facing lookup responsive while still riding out a single bad gateway.
const (
	defaultMaxAttempts   = 3
	defaultBackoff       = 500 * time.Millisecond
	defaultTimeout       = 20 * time.Second
	maxRetryAfterWait    = 5 * time.Second
	defaultRetryAfterCap = maxRetryAfterWait
)

// Error is a failed call to an external service. Message carries the technical
// detail for logs; UserMessage renders the sentence shown to a person.
type Error struct {
	// Service is the provider's display name, e.g. "Cover Art Archive".
	Service string
	Kind    Kind
	// Status is the upstream HTTP status, or 0 when the request never
	// produced a response.
	Status int
	// Attempts is how many requests were made before giving up.
	Attempts int
	Err      error

	// retryable marks a failure worth another attempt; retryAfter is the delay
	// the provider asked for, if any. Both are internal to the retry loop.
	retryable  bool
	retryAfter time.Duration
}

func (e *Error) Error() string {
	svc := e.Service
	if svc == "" {
		svc = "upstream service"
	}
	switch {
	case e.Status > 0 && e.Err != nil:
		return fmt.Sprintf("%s: status %d after %d attempt(s): %v", svc, e.Status, e.Attempts, e.Err)
	case e.Status > 0:
		return fmt.Sprintf("%s: status %d after %d attempt(s)", svc, e.Status, e.Attempts)
	case e.Err != nil:
		return fmt.Sprintf("%s: %v", svc, e.Err)
	default:
		return svc + ": request failed"
	}
}

func (e *Error) Unwrap() error { return e.Err }

// WrapError builds an *Error for a provider whose client does not use Client —
// a hand-rolled client that classified its own failure but cannot reuse the
// retry loop. Status may be 0 when no response was received.
//
// The retry bookkeeping is deliberately left zero: the caller already finished
// trying, and this only exists so the failure reaches callers in the shape
// HTTPStatus and UserMessage understand.
func WrapError(service string, kind Kind, status int, err error) *Error {
	return &Error{Service: service, Kind: kind, Status: status, Err: err, Attempts: 1}
}

// UserMessage is a complete, human-readable sentence naming the service and
// what to do about it. It deliberately omits status codes and Go error text.
func (e *Error) UserMessage() string {
	svc := e.Service
	if svc == "" {
		svc = "The external service"
	}
	switch e.Kind {
	case KindRateLimited:
		return svc + " is receiving too many requests right now. Wait a moment and try again."
	case KindTimeout:
		return svc + " took too long to respond. Try again in a moment."
	case KindUnreachable:
		return svc + " could not be reached. Check the server's internet connection and try again."
	case KindRejected:
		return svc + " rejected this request. The identifier may be wrong or no longer exist."
	case KindBadResponse:
		return svc + " returned a response that could not be read. Try again in a moment."
	default:
		return svc + " is temporarily unavailable. Try again in a few minutes."
	}
}

// HTTPStatus is the status to answer with for e: 502 by default, mirroring the
// upstream condition where a more precise code exists.
func (e *Error) HTTPStatus() int {
	switch e.Kind {
	case KindRateLimited:
		return http.StatusTooManyRequests
	case KindTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

// HTTPStatus is the status to answer with for err: 502 by default (also for any
// error that is not an *Error), or a more precise code from (*Error).HTTPStatus.
func HTTPStatus(err error) int {
	var uerr *Error
	if !errors.As(err, &uerr) {
		return http.StatusBadGateway
	}
	return uerr.HTTPStatus()
}

// IsRejected reports whether err is the provider refusing the request itself
// (a 4xx other than 429). Callers use it to treat "no data for this id" as an
// empty result while still propagating genuine outages.
func IsRejected(err error) bool {
	var uerr *Error
	return errors.As(err, &uerr) && uerr.Kind == KindRejected
}

// UserMessage returns err's human-readable sentence, or fallback when err is
// not an upstream error (so callers can hand any error to the UI safely).
func UserMessage(err error, fallback string) string {
	var uerr *Error
	if errors.As(err, &uerr) {
		return uerr.UserMessage()
	}
	return fallback
}

// Cfg configures a Client. Only Service is really needed; every other field has
// a sane default. The optional override fields are primarily test seams — set
// them at construction rather than mutating a Client after New.
type Cfg struct {
	// Service is the provider's display name, used in error messages.
	Service   string
	UserAgent string
	// RPS throttles requests to this many per second (burst 1). A value <= 0
	// leaves the client unthrottled.
	RPS float64

	// Client overrides the default *http.Client (20s timeout).
	Client *http.Client
	// Limiter overrides the limiter built from RPS.
	Limiter *rate.Limiter
	// MaxAttempts bounds the total number of requests per call (1 = no retry);
	// <= 0 uses the default of 3.
	MaxAttempts int
	// Backoff is the delay before the second attempt; it doubles thereafter.
	// <= 0 uses the default of 500ms.
	Backoff time.Duration
	// RetryAfterCap bounds how long a Retry-After header can make us wait — a
	// provider asking for minutes must not hang a user-facing request. <= 0
	// uses the default of 5s.
	RetryAfterCap time.Duration
	// Wait sleeps for d, honouring ctx. Overridable so tests assert backoff
	// timing without real delay; nil uses a ctx-aware sleep.
	Wait func(ctx context.Context, d time.Duration) error
}

// Client performs throttled, retrying GET requests against one external
// service. Construct it with New; the zero value is not usable.
type Client struct {
	service       string
	userAgent     string
	client        *http.Client
	limiter       *rate.Limiter
	maxAttempts   int
	backoff       time.Duration
	retryAfterCap time.Duration
	wait          func(ctx context.Context, d time.Duration) error
}

// New returns a Client for the service described by cfg, filling in defaults
// for any field cfg leaves zero.
func New(cfg Cfg) *Client {
	c := &Client{
		service:       cfg.Service,
		userAgent:     cfg.UserAgent,
		client:        cfg.Client,
		limiter:       cfg.Limiter,
		maxAttempts:   cfg.MaxAttempts,
		backoff:       cfg.Backoff,
		retryAfterCap: cfg.RetryAfterCap,
		wait:          cfg.Wait,
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: defaultTimeout}
	}
	if c.limiter == nil && cfg.RPS > 0 {
		c.limiter = rate.NewLimiter(rate.Limit(cfg.RPS), 1)
	}
	if c.maxAttempts < 1 {
		c.maxAttempts = defaultMaxAttempts
	}
	if c.backoff <= 0 {
		c.backoff = defaultBackoff
	}
	if c.retryAfterCap <= 0 {
		c.retryAfterCap = defaultRetryAfterCap
	}
	if c.wait == nil {
		c.wait = sleep
	}
	return c
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Get issues a throttled GET, retrying transient failures. Any status in
// allowStatus is returned to the caller as a successful response (used for
// "404 = no data here", which is not an error for most providers); every other
// non-2xx becomes an *Error.
//
// On success the caller owns resp.Body and must close it.
func (c *Client) Get(ctx context.Context, url string, header http.Header, allowStatus ...int) (*http.Response, error) {
	attempts := c.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	backoff := c.backoff
	var last *Error

	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			wait := backoff
			if last != nil && last.retryAfter > 0 {
				wait = last.retryAfter
			}
			if err := c.waitFor(ctx, wait); err != nil {
				return nil, err
			}
			backoff *= 2
		}

		resp, err := c.do(ctx, url, header)
		if err != nil {
			// Throttle/context failures are ours, not the provider's: report
			// them as-is and do not burn retries on them.
			var uerr *Error
			if !errors.As(err, &uerr) {
				return nil, err
			}
			uerr.Attempts = attempt
			if !uerr.retryable {
				return nil, uerr
			}
			last = uerr
			continue
		}

		if resp.StatusCode < 300 || containsStatus(allowStatus, resp.StatusCode) {
			return resp, nil
		}

		serr := c.statusError(resp)
		_ = resp.Body.Close()
		serr.Attempts = attempt
		if !serr.retryable {
			return nil, serr
		}
		last = serr
	}

	if last == nil { // unreachable: the loop always runs at least once
		last = &Error{Service: c.service, Kind: KindUnavailable, Attempts: attempts}
	}
	last.Attempts = attempts
	return nil, last
}

func (c *Client) waitFor(ctx context.Context, d time.Duration) error {
	if c.wait != nil {
		return c.wait(ctx, d)
	}
	return sleep(ctx, d)
}

// do performs one throttled request, classifying transport failures.
func (c *Client) do(ctx context.Context, url string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	for k, vals := range header {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("%s throttle: %w", c.service, err)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		// A cancelled/expired caller context is the caller's business, not a
		// provider fault — don't retry it and don't relabel it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		kind := KindUnreachable
		if isTimeout(err) {
			kind = KindTimeout
		}
		return nil, &Error{Service: c.service, Kind: kind, Err: err, retryable: true}
	}
	return resp, nil
}

// statusError classifies a non-2xx response. 5xx and 429 are transient (worth
// a retry); every other 4xx is a refusal we must not hammer.
func (c *Client) statusError(resp *http.Response) *Error {
	e := &Error{Service: c.service, Status: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
		e.retryable = true
		e.retryAfter = c.retryAfterFrom(resp)
	case resp.StatusCode >= 500:
		e.Kind = KindUnavailable
		e.retryable = true
		e.retryAfter = c.retryAfterFrom(resp)
	default:
		e.Kind = KindRejected
	}
	return e
}

// retryAfterFrom reads the Retry-After header (delay-seconds or HTTP-date),
// clamped to the configured cap. Zero means "use the normal backoff".
func (c *Client) retryAfterFrom(resp *http.Response) time.Duration {
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	var wait time.Duration
	if secs, err := strconv.Atoi(raw); err == nil {
		wait = time.Duration(secs) * time.Second
	} else if when, err := http.ParseTime(raw); err == nil {
		// An HTTP-date is absolute; relative waits need the response's own
		// clock reference, so fall back to the backoff when it is in the past.
		wait = time.Until(when)
	}
	if wait <= 0 {
		return 0
	}
	limit := c.retryAfterCap
	if limit <= 0 {
		limit = defaultRetryAfterCap
	}
	if wait > limit {
		wait = limit
	}
	return wait
}

// BadResponse wraps a parse failure on an otherwise-successful response, so a
// malformed provider payload reads like any other upstream problem.
func (c *Client) BadResponse(err error) error {
	return &Error{Service: c.service, Kind: KindBadResponse, Err: err}
}

func isTimeout(err error) bool {
	var terr interface{ Timeout() bool }
	return errors.As(err, &terr) && terr.Timeout()
}

func containsStatus(list []int, status int) bool {
	for _, s := range list {
		if s == status {
			return true
		}
	}
	return false
}
