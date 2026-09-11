// Package problemjson writes RFC 9457 ("Problem Details for HTTP APIs")
// application/problem+json responses.
//
// It replaces ad-hoc {"error":..., "code":...} error shapes with one
// consistent, spec-shaped error body: a stable, dereferenceable-looking but
// never-fetched Type URI in place of a loose code string, a human title/detail,
// and the request path as instance.
//
// Construct a Writer with New, giving it the base URI that namespaces every
// problem's Type and, optionally, titles for your own slugs (merged over the
// built-in defaults). The zero Writer is not usable — New requires a base URI.
//
// This is a handler-layer helper: a handler calls it to emit a deliberate,
// precise error body. It is distinct from a transport-layer error-wrapping
// middleware, which is a catch-all that rewraps bare error responses; a given
// route group should use one or the other, not both (a middleware that rewraps
// bodies would otherwise double-encode this package's problem+json).
package problemjson

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Mode selects how much a problem body reveals to the client.
type Mode int

const (
	// ModeDev renders the full, handler-supplied detail (and validation
	// fields). It is the zero value, so a Writer configured without a Mode
	// behaves as it did before this option existed.
	ModeDev Mode = iota
	// ModeMasked strips the detail and validation fields and replaces the
	// type/title with a generic identity (SlugMasked), leaving only the HTTP
	// status, the request instance, and the correlation reference. Use it in
	// production so internal specifics never reach the client.
	ModeMasked
)

// Library-owned slugs. New ships a built-in title for exactly these; a caller
// supplies titles for its own slugs (and may override these) via Cfg.Titles.
const (
	SlugNotFound            = "not_found"
	SlugValidationError     = "validation_error"
	SlugInternal            = "internal"
	SlugUnauthorized        = "unauthorized"
	SlugForbidden           = "forbidden"
	SlugConflict            = "conflict"
	SlugRateLimited         = "rate_limited"
	SlugUnavailable         = "unavailable"
	SlugUpstreamError       = "upstream_error"
	SlugUpstreamRateLimited = "upstream_rate_limited"
	SlugUpstreamTimeout     = "upstream_timeout"
)

// SlugMasked is the generic identity every problem collapses to under
// ModeMasked. Its title is overridable via Cfg.Titles like any other slug.
const SlugMasked = "error"

// defaultTitles are the human titles for the library-owned slugs above.
var defaultTitles = map[string]string{
	SlugNotFound:            "Not found",
	SlugValidationError:     "Validation error",
	SlugInternal:            "Internal error",
	SlugUnauthorized:        "Unauthorized",
	SlugForbidden:           "Forbidden",
	SlugConflict:            "Conflict",
	SlugRateLimited:         "Rate limited",
	SlugUnavailable:         "Unavailable",
	SlugUpstreamError:       "Upstream error",
	SlugUpstreamRateLimited: "Upstream rate limited",
	SlugUpstreamTimeout:     "Upstream timeout",
	SlugMasked:              "An error occurred",
}

// Details is an RFC 9457 problem detail object.
type Details struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	Reference string `json:"reference,omitempty"`
}

// FieldError is one field-level validation failure. Pointer names the failing
// field using JSON Pointer (RFC 6901) syntax — e.g. "/paths" or "/paths/0" —
// whether or not the request actually carried a JSON body: a query parameter or
// multipart form field is addressed the same way, by the name it would have in
// the endpoint's JSON shape, since callers only need to know which field failed.
type FieldError struct {
	Pointer string `json:"pointer"`
	Detail  string `json:"detail"`
}

// ValidationDetails extends Details with field-level errors, for 422 responses
// to a request that is well-formed but fails validation (over a size cap, an
// unknown enum value, ...).
type ValidationDetails struct {
	Details
	Errors []FieldError `json:"errors,omitempty"`
}

// UpstreamError is the behaviour WriteUpstream needs from a failed outbound
// call: an HTTP status and a user-facing message. Any error whose chain
// implements it is understood, so this package needs no import of the client
// that produced the error.
type UpstreamError interface {
	HTTPStatus() int
	UserMessage() string
}

// Cfg configures a Writer.
type Cfg struct {
	// BaseURI namespaces every problem's Type (BaseURI + "/" + slug). Required;
	// it is an opaque namespace only the caller owns, never fetched by clients.
	BaseURI string
	// Titles maps a slug to its human title, merged over the built-in defaults
	// for the library-owned slugs — supply your own slugs, or override a default.
	Titles map[string]string
	// RequestID, when set, is called per response to obtain a correlation
	// reference (typically the caller's request-id / trace-id from context or
	// a header). Its non-empty result is written to Details.Reference so a
	// masked client-facing error can still be traced to the real detail in
	// logs. Return "" (or leave RequestID nil) to omit the reference.
	RequestID func(*http.Request) string
	// Mode selects verbose (ModeDev, the zero value) vs masked (ModeMasked)
	// rendering for every response this Writer emits.
	Mode Mode
}

// Writer emits problem+json responses for one base URI and title set.
type Writer struct {
	baseURI   string
	titles    map[string]string
	requestID func(*http.Request) string
	mode      Mode
}

// New returns a Writer configured by cfg. It errors when BaseURI is empty: the
// base URI is a namespace only the caller owns, so there is no sane default.
func New(cfg Cfg) (*Writer, error) {
	if cfg.BaseURI == "" {
		return nil, errors.New("problemjson: Cfg.BaseURI is required")
	}
	titles := make(map[string]string, len(defaultTitles)+len(cfg.Titles))
	for slug, title := range defaultTitles {
		titles[slug] = title
	}
	for slug, title := range cfg.Titles {
		titles[slug] = title
	}
	return &Writer{
		baseURI:   strings.TrimRight(cfg.BaseURI, "/"),
		titles:    titles,
		requestID: cfg.RequestID,
		mode:      cfg.Mode,
	}, nil
}

// Write emits an application/problem+json response. Type is built from slug
// (BaseURI + "/" + slug) and Title is derived from slug via TitleFor; Instance
// is always the request path, so a client can tell which call failed without
// re-reading its own request.
func (wr *Writer) Write(w http.ResponseWriter, r *http.Request, status int, slug, detail string) {
	d := Details{
		Type:      wr.TypeURI(slug),
		Title:     wr.TitleFor(slug),
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		Reference: wr.reference(r),
	}
	if wr.mode == ModeMasked {
		d = wr.mask(d)
	}
	writeProblem(w, status, d)
}

// WriteValidation reports a well-formed but invalid request: always 422,
// optionally itemising which fields failed and why.
func (wr *Writer) WriteValidation(w http.ResponseWriter, r *http.Request, detail string, fields ...FieldError) {
	const status = http.StatusUnprocessableEntity
	d := Details{
		Type:      wr.TypeURI(SlugValidationError),
		Title:     wr.TitleFor(SlugValidationError),
		Status:    status,
		Detail:    detail,
		Instance:  r.URL.Path,
		Reference: wr.reference(r),
	}
	if wr.mode == ModeMasked {
		writeProblem(w, status, wr.mask(d))
		return
	}
	writeProblem(w, status, ValidationDetails{Details: d, Errors: fields})
}

// WriteUpstream reports a failed call to an external service: 429 when the
// provider is rate-limiting, 504 on a timeout, otherwise 502 (see
// UpstreamError.HTTPStatus). Detail is the error's human-readable sentence, or
// fallback for an error that does not implement UpstreamError — never a raw Go
// error.
func (wr *Writer) WriteUpstream(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	status, detail := http.StatusBadGateway, fallback
	var ue UpstreamError
	if errors.As(err, &ue) {
		status, detail = ue.HTTPStatus(), ue.UserMessage()
	}
	slug := SlugUpstreamError
	switch status {
	case http.StatusTooManyRequests:
		slug = SlugUpstreamRateLimited
	case http.StatusGatewayTimeout:
		slug = SlugUpstreamTimeout
	}
	wr.Write(w, r, status, slug, detail)
}

// TypeURI builds the stable Type URI (BaseURI + "/" + slug) a problem carries.
// It is Slug's inverse, exported for a caller that must build a Details body
// without a ResponseWriter to hand to Write — e.g. a router-level error-envelope
// fallback that only has a status code and a plain-text message to work with.
func (wr *Writer) TypeURI(slug string) string {
	return wr.baseURI + "/" + slug
}

// TitleFor returns the human title for slug, or slug itself when it is not one
// of the configured titles.
func (wr *Writer) TitleFor(slug string) string {
	if title, ok := wr.titles[slug]; ok {
		return title
	}
	return slug
}

// mask rewrites d for ModeMasked: a generic type/title and no detail,
// preserving the status, instance, and reference so the response is still
// classifiable and traceable without leaking specifics.
func (wr *Writer) mask(d Details) Details {
	return Details{
		Type:      wr.TypeURI(SlugMasked),
		Title:     wr.TitleFor(SlugMasked),
		Status:    d.Status,
		Instance:  d.Instance,
		Reference: d.Reference,
	}
}

// reference resolves the correlation reference for r, or "" when no extractor
// is configured. Callers place it in Details.Reference (omitted when empty).
func (wr *Writer) reference(r *http.Request) string {
	if wr.requestID == nil {
		return ""
	}
	return wr.requestID(r)
}

// Slug returns the last path segment of a problem's Type URI — the old "code"
// string — so a client can switch on it without parsing a URI. It needs no
// configuration and so is a package function, the inverse of Writer.TypeURI.
func Slug(typeURI string) string {
	if i := strings.LastIndex(typeURI, "/"); i >= 0 {
		return typeURI[i+1:]
	}
	return typeURI
}

// writeProblem sets the problem+json content type and status before encoding
// body, satisfying every caller's requirement to set headers before WriteHeader.
func writeProblem(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
