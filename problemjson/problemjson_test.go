package problemjson

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testBaseURI = "https://example.test/probs"

// stubUpstream satisfies UpstreamError without importing any client package —
// exactly how a real *outbound.Error reaches WriteUpstream.
type stubUpstream struct {
	status int
	msg    string
}

func (s stubUpstream) Error() string      { return s.msg }
func (s stubUpstream) HTTPStatus() int    { return s.status }
func (s stubUpstream) UserMessage() string { return s.msg }

func newTestWriter(t *testing.T) *Writer {
	t.Helper()
	// "queue_full" is a caller-owned slug, unknown to the library defaults.
	wr, err := New(Cfg{BaseURI: testBaseURI, Titles: map[string]string{"queue_full": "Queue full"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return wr
}

func TestNewRequiresBaseURI(t *testing.T) {
	if _, err := New(Cfg{}); err == nil {
		t.Fatal("New must reject an empty BaseURI")
	}
	if _, err := New(Cfg{BaseURI: testBaseURI}); err != nil {
		t.Fatalf("New with a BaseURI must succeed, got %v", err)
	}
}

func TestWrite(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	wr.Write(w, req, http.StatusNotFound, SlugNotFound, "the widget does not exist")

	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}

	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	want := Details{
		Type:     testBaseURI + "/not_found",
		Title:    "Not found",
		Status:   http.StatusNotFound,
		Detail:   "the widget does not exist",
		Instance: "/api/v0/x",
	}
	if got != want {
		t.Fatalf("Details = %+v, want %+v", got, want)
	}
}

func TestWriteValidation(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	wr.WriteValidation(w, req, "2 fields failed validation",
		FieldError{Pointer: "/paths/0", Detail: "must not be empty"},
		FieldError{Pointer: "/type", Detail: "unknown picture type"},
	)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}

	var got ValidationDetails
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	wantDetails := Details{
		Type:     testBaseURI + "/validation_error",
		Title:    "Validation error",
		Status:   http.StatusUnprocessableEntity,
		Detail:   "2 fields failed validation",
		Instance: "/api/v0/x",
	}
	if got.Details != wantDetails {
		t.Fatalf("Details = %+v, want %+v", got.Details, wantDetails)
	}
	wantErrors := []FieldError{
		{Pointer: "/paths/0", Detail: "must not be empty"},
		{Pointer: "/type", Detail: "unknown picture type"},
	}
	if len(got.Errors) != len(wantErrors) || got.Errors[0] != wantErrors[0] || got.Errors[1] != wantErrors[1] {
		t.Fatalf("Errors = %+v, want %+v", got.Errors, wantErrors)
	}
}

func TestWriteUpstreamRateLimited(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v0/radio/browse", nil)
	w := httptest.NewRecorder()

	stub := stubUpstream{status: http.StatusTooManyRequests, msg: "Example Service is receiving too many requests right now."}
	wr.WriteUpstream(w, req, stub, "fallback message")

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	if slug := Slug(got.Type); slug != SlugUpstreamRateLimited {
		t.Fatalf("Slug(%q) = %q, want %q", got.Type, slug, SlugUpstreamRateLimited)
	}
	if got.Status != http.StatusTooManyRequests {
		t.Fatalf("Status = %d, want %d", got.Status, http.StatusTooManyRequests)
	}
	if want := wr.TitleFor(SlugUpstreamRateLimited); got.Title != want {
		t.Fatalf("Title = %q, want %q", got.Title, want)
	}
	if got.Detail != stub.msg {
		t.Fatalf("Detail = %q, want %q", got.Detail, stub.msg)
	}
}

func TestWriteUpstreamTimeoutUsesTimeoutSlug(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	stub := stubUpstream{status: http.StatusGatewayTimeout, msg: "Example Service took too long to respond."}
	wr.WriteUpstream(w, req, stub, "fallback message")

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusGatewayTimeout)
	}
	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	if slug := Slug(got.Type); slug != SlugUpstreamTimeout {
		t.Fatalf("Slug(%q) = %q, want %q", got.Type, slug, SlugUpstreamTimeout)
	}
}

func TestWriteUpstreamFallsBackTo502ForNonUpstreamError(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v0/radio/browse", nil)
	w := httptest.NewRecorder()

	wr.WriteUpstream(w, req, errNotUpstream, "fallback message")

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}

	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	if slug := Slug(got.Type); slug != SlugUpstreamError {
		t.Fatalf("Slug(%q) = %q, want %q", got.Type, slug, SlugUpstreamError)
	}
	if got.Detail != "fallback message" {
		t.Fatalf("Detail = %q, want fallback message", got.Detail)
	}
	// Title must match TitleFor(slug) — the same title a direct Write for this
	// slug would carry — not http.StatusText(status), which would say "Bad
	// Gateway" and diverge from other responses sharing this type URI.
	if want := wr.TitleFor(SlugUpstreamError); got.Title != want {
		t.Fatalf("Title = %q, want %q", got.Title, want)
	}
}

func TestSlugRoundTrips(t *testing.T) {
	for _, slug := range []string{SlugNotFound, SlugValidationError, SlugUpstreamRateLimited} {
		typeURI := testBaseURI + "/" + slug
		if got := Slug(typeURI); got != slug {
			t.Errorf("Slug(%q) = %q, want %q", typeURI, got, slug)
		}
	}
	if got := Slug("no-slash-here"); got != "no-slash-here" {
		t.Errorf(`Slug("no-slash-here") = %q, want input echoed back`, got)
	}
}

func TestTypeURI(t *testing.T) {
	wr := newTestWriter(t)
	for _, slug := range []string{SlugNotFound, SlugValidationError, SlugForbidden} {
		want := testBaseURI + "/" + slug
		if got := wr.TypeURI(slug); got != want {
			t.Errorf("TypeURI(%q) = %q, want %q", slug, got, want)
		}
		// TypeURI and Slug must round-trip: a router-level fallback builds a
		// Type from a bare status via TypeURI, and a client reads it back via
		// Slug, exactly as it would for a Details built by Write.
		if got := Slug(wr.TypeURI(slug)); got != slug {
			t.Errorf("Slug(TypeURI(%q)) = %q, want %q", slug, got, slug)
		}
	}
}

func TestTypeURITrimsTrailingSlash(t *testing.T) {
	wr, err := New(Cfg{BaseURI: "https://example.test/probs/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := wr.TypeURI(SlugNotFound); got != "https://example.test/probs/not_found" {
		t.Fatalf("TypeURI = %q, want no doubled slash", got)
	}
}

func TestTitleFor(t *testing.T) {
	wr := newTestWriter(t)
	for slug, want := range map[string]string{
		SlugValidationError:     "Validation error",
		SlugNotFound:            "Not found",
		SlugInternal:            "Internal error",
		SlugUpstreamError:       "Upstream error",
		SlugUpstreamRateLimited: "Upstream rate limited",
		SlugForbidden:           "Forbidden",
		SlugRateLimited:         "Rate limited",
		SlugUnavailable:         "Unavailable",
		SlugUpstreamTimeout:     "Upstream timeout",
		"queue_full":            "Queue full", // caller-supplied
	} {
		if got := wr.TitleFor(slug); got != want {
			t.Errorf("TitleFor(%q) = %q, want %q", slug, got, want)
		}
	}
	if got := wr.TitleFor("some_unmapped_slug"); got != "some_unmapped_slug" {
		t.Errorf("TitleFor(unmapped) = %q, want the slug echoed back", got)
	}
}

func TestTitlesOverrideDefaults(t *testing.T) {
	wr, err := New(Cfg{BaseURI: testBaseURI, Titles: map[string]string{SlugNotFound: "Nope"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := wr.TitleFor(SlugNotFound); got != "Nope" {
		t.Fatalf("a caller title must override the built-in default, got %q", got)
	}
}

// errNotUpstream is a plain error that does not implement UpstreamError.
var errNotUpstream = &plainErr{"plain error"}

type plainErr struct{ msg string }

func (e *plainErr) Error() string { return e.msg }

func TestWriteIncludesReference(t *testing.T) {
	wr, err := New(Cfg{
		BaseURI:   testBaseURI,
		RequestID: func(*http.Request) string { return "req-123" },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	wr.Write(w, req, http.StatusInternalServerError, SlugInternal, "boom")

	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if got.Reference != "req-123" {
		t.Fatalf("Reference = %q, want %q", got.Reference, "req-123")
	}
}

func TestReferenceOmittedWhenAbsent(t *testing.T) {
	cases := map[string]func(*http.Request) string{
		"nil extractor":   nil,
		"empty extractor": func(*http.Request) string { return "" },
	}
	for name, rid := range cases {
		t.Run(name, func(t *testing.T) {
			wr, err := New(Cfg{BaseURI: testBaseURI, RequestID: rid})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
			w := httptest.NewRecorder()

			wr.Write(w, req, http.StatusNotFound, SlugNotFound, "nope")

			var m map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, ok := m["reference"]; ok {
				t.Fatalf("reference key must be omitted, body=%s", w.Body.String())
			}
		})
	}
}

func TestWriteValidationIncludesReference(t *testing.T) {
	wr, err := New(Cfg{
		BaseURI:   testBaseURI,
		RequestID: func(*http.Request) string { return "req-xyz" },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	wr.WriteValidation(w, req, "bad", FieldError{Pointer: "/qty", Detail: "too big"})

	var got ValidationDetails
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reference != "req-xyz" {
		t.Fatalf("Reference = %q, want req-xyz", got.Reference)
	}
	if len(got.Errors) != 1 {
		t.Fatalf("want 1 field error, got %d", len(got.Errors))
	}
}

func TestMaskedWrite(t *testing.T) {
	wr, err := New(Cfg{
		BaseURI:   testBaseURI,
		Mode:      ModeMasked,
		RequestID: func(*http.Request) string { return "req-9" },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v0/orders/42", nil)
	w := httptest.NewRecorder()

	wr.Write(w, req, http.StatusInternalServerError, SlugInternal, "db: connection refused")

	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	want := Details{
		Type:      testBaseURI + "/error",
		Title:     "An error occurred",
		Status:    http.StatusInternalServerError,
		Instance:  "/api/v0/orders/42",
		Reference: "req-9",
	}
	if got != want {
		t.Fatalf("masked Details = %+v, want %+v", got, want)
	}
}

func TestMaskedWriteUpstreamPreservesStatus(t *testing.T) {
	wr, err := New(Cfg{BaseURI: testBaseURI, Mode: ModeMasked})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v0/pay", nil)
	w := httptest.NewRecorder()

	stub := stubUpstream{status: http.StatusGatewayTimeout, msg: "provider slow"}
	wr.WriteUpstream(w, req, stub, "fallback")

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("HTTP status = %d, want 504", w.Code)
	}
	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != testBaseURI+"/error" {
		t.Fatalf("Type = %q, want masked", got.Type)
	}
	if got.Status != http.StatusGatewayTimeout {
		t.Fatalf("body Status = %d, want 504", got.Status)
	}
	if got.Detail != "" {
		t.Fatalf("Detail = %q, want empty (masked)", got.Detail)
	}
}

func TestMaskedTitleOverride(t *testing.T) {
	wr, err := New(Cfg{
		BaseURI: testBaseURI,
		Mode:    ModeMasked,
		Titles:  map[string]string{SlugMasked: "Request failed"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	wr.Write(w, req, http.StatusBadRequest, SlugValidationError, "nope")

	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "Request failed" {
		t.Fatalf("Title = %q, want override", got.Title)
	}
}

func TestMaskedWriteValidationDropsFields(t *testing.T) {
	wr, err := New(Cfg{
		BaseURI:   testBaseURI,
		Mode:      ModeMasked,
		RequestID: func(*http.Request) string { return "req-v" },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v0/orders", nil)
	w := httptest.NewRecorder()

	wr.WriteValidation(w, req, "too many items",
		FieldError{Pointer: "/items", Detail: "max 10"})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", w.Code)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["errors"]; ok {
		t.Fatalf("masked validation must not include errors[], body=%s", w.Body.String())
	}
	var got Details
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := Details{
		Type:      testBaseURI + "/error",
		Title:     "An error occurred",
		Status:    http.StatusUnprocessableEntity,
		Instance:  "/api/v0/orders",
		Reference: "req-v",
	}
	if got != want {
		t.Fatalf("masked validation Details = %+v, want %+v", got, want)
	}
}
