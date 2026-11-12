package problemjson

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

const testBaseURI = "https://example.test/probs"

// stubUpstream satisfies UpstreamError without importing any client package —
// exactly how a real *outbound.Error reaches WriteUpstream.
type stubUpstream struct {
	status int
	msg    string
}

func (s stubUpstream) Error() string       { return s.msg }
func (s stubUpstream) HTTPStatus() int     { return s.status }
func (s stubUpstream) UserMessage() string { return s.msg }

// stubUpstreamReason also names its reason, like a real *outbound.Error, so it
// exercises the dev-mode "reason" enrichment path.
type stubUpstreamReason struct {
	stubUpstream
	reason string
}

func (s stubUpstreamReason) UpstreamReason() string { return s.reason }

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

// TestInstanceKeepsEscapedPath pins instance to the escaped request path: RFC
// 9457 §3.1.5 requires a URI reference, so "/files/a b" (decoded) is invalid
// and "/a%2Fb" must not collapse to "/a/b". Both modes and the Details handed
// to OnProblem carry the same value.
func TestInstanceKeepsEscapedPath(t *testing.T) {
	tcs := []struct {
		name, target, want string
	}{
		{name: "escaped space", target: "/files/a%20b", want: "/files/a%20b"},
		{name: "escaped slash", target: "/a%2Fb", want: "/a%2Fb"},
		{name: "plain path", target: "/api/v0/x", want: "/api/v0/x"},
	}
	modes := []struct {
		name string
		mode Mode
	}{{"dev", ModeDev}, {"masked", ModeMasked}}
	for _, m := range modes {
		for _, tc := range tcs {
			t.Run(m.name+"/"+tc.name, func(t *testing.T) {
				var notified Details
				wr, err := New(Cfg{BaseURI: testBaseURI, Mode: m.mode, OnProblem: func(_ *http.Request, p Problem) { notified = p.Details }})
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				w := httptest.NewRecorder()
				wr.Write(w, httptest.NewRequest(http.MethodGet, tc.target, nil), http.StatusNotFound, SlugNotFound, "gone")

				var got Details
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
				}
				if got.Instance != tc.want {
					t.Errorf("body instance = %q, want %q", got.Instance, tc.want)
				}
				if notified.Instance != tc.want {
					t.Errorf("OnProblem instance = %q, want %q", notified.Instance, tc.want)
				}
			})
		}
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

func TestWriteUpstreamDevIncludesReason(t *testing.T) {
	wr := newTestWriter(t) // dev mode is the zero value
	req := httptest.NewRequest(http.MethodGet, "/api/v0/cover/42", nil)
	w := httptest.NewRecorder()

	stub := stubUpstreamReason{
		stubUpstream: stubUpstream{status: http.StatusBadGateway, msg: "Cover Art Archive could not be reached."},
		reason:       "unreachable",
	}
	wr.WriteUpstream(w, req, stub, "fallback message")

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	var got upstreamDetails
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if got.Reason != "unreachable" {
		t.Fatalf("reason = %q, want %q; body=%s", got.Reason, "unreachable", w.Body.String())
	}
	// The reason is additive: the coarse slug and human detail are unchanged, so
	// four 502 kinds still share one type URI while the reason names the cause.
	if slug := Slug(got.Type); slug != SlugUpstreamError {
		t.Fatalf("Slug(%q) = %q, want %q", got.Type, slug, SlugUpstreamError)
	}
	if got.Detail != stub.msg {
		t.Fatalf("Detail = %q, want %q", got.Detail, stub.msg)
	}
}

func TestWriteUpstreamOmitsReasonWithoutMethod(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	// stubUpstream implements UpstreamError but not UpstreamReason.
	wr.WriteUpstream(w, req, stubUpstream{status: http.StatusBadGateway, msg: "down"}, "fallback")

	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["reason"]; ok {
		t.Fatalf("reason must be omitted when the error can't name it, body=%s", w.Body.String())
	}
}

func TestWriteUpstreamOmitsEmptyReason(t *testing.T) {
	wr := newTestWriter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	stub := stubUpstreamReason{stubUpstream: stubUpstream{status: http.StatusBadGateway, msg: "down"}}
	wr.WriteUpstream(w, req, stub, "fallback")

	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["reason"]; ok {
		t.Fatalf("an empty reason must be omitted, body=%s", w.Body.String())
	}
}

func TestMaskedWriteUpstreamOmitsReason(t *testing.T) {
	wr, err := New(Cfg{BaseURI: testBaseURI, Mode: ModeMasked})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v0/x", nil)
	w := httptest.NewRecorder()

	stub := stubUpstreamReason{
		stubUpstream: stubUpstream{status: http.StatusBadGateway, msg: "boom"},
		reason:       "unreachable",
	}
	wr.WriteUpstream(w, req, stub, "fallback")

	var m map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["reason"]; ok {
		t.Fatalf("masked mode must not leak the reason, body=%s", w.Body.String())
	}
	if _, ok := m["detail"]; ok {
		t.Fatalf("masked mode must not include detail, body=%s", w.Body.String())
	}
}

// TestOnProblemReceivesUnmaskedDetails pins that a masked response stays
// traceable: OnProblem sees the real detail on every write path while the
// client body stays masked, and both carry the same reference.
func TestOnProblemReceivesUnmaskedDetails(t *testing.T) {
	type call struct {
		path string
		d    Details
	}
	var calls []call
	wr, err := New(Cfg{
		BaseURI:   testBaseURI,
		Mode:      ModeMasked,
		RequestID: func(r *http.Request) string { return "ref-" + r.URL.Path },
		OnProblem: func(r *http.Request, p Problem) { calls = append(calls, call{r.URL.Path, p.Details}) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tests := []struct {
		name       string
		write      func(w http.ResponseWriter, r *http.Request)
		wantStatus int
		wantSlug   string
		wantDetail string
	}{
		{
			name: "Write",
			write: func(w http.ResponseWriter, r *http.Request) {
				wr.Write(w, r, http.StatusInternalServerError, SlugInternal, "db: connection refused")
			},
			wantStatus: http.StatusInternalServerError, wantSlug: SlugInternal, wantDetail: "db: connection refused",
		},
		{
			name: "WriteValidation",
			write: func(w http.ResponseWriter, r *http.Request) {
				wr.WriteValidation(w, r, "too many items", FieldError{Pointer: "/items", Detail: "max 10"})
			},
			wantStatus: http.StatusUnprocessableEntity, wantSlug: SlugValidationError, wantDetail: "too many items",
		},
		{
			name: "WriteUpstream",
			write: func(w http.ResponseWriter, r *http.Request) {
				wr.WriteUpstream(w, r, stubUpstreamReason{
					stubUpstream: stubUpstream{status: http.StatusGatewayTimeout, msg: "provider slow"},
					reason:       "timeout",
				}, "fallback")
			},
			wantStatus: http.StatusGatewayTimeout, wantSlug: SlugUpstreamTimeout, wantDetail: "provider slow",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls = nil
			path := "/api/v0/" + tc.name
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()

			tc.write(w, req)

			var body Details
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v; body=%s", err, w.Body.String())
			}
			if body.Detail != "" || Slug(body.Type) != SlugMasked {
				t.Fatalf("body not masked: %+v", body)
			}
			if len(calls) != 1 {
				t.Fatalf("OnProblem called %d times, want 1", len(calls))
			}
			got := calls[0]
			want := Details{
				Type:      testBaseURI + "/" + tc.wantSlug,
				Title:     wr.TitleFor(tc.wantSlug),
				Status:    tc.wantStatus,
				Detail:    tc.wantDetail,
				Instance:  path,
				Reference: "ref-" + path,
			}
			if got.path != path || got.d != want {
				t.Fatalf("OnProblem got (%s, %+v), want (%s, %+v)", got.path, got.d, path, want)
			}
			if got.d.Reference != body.Reference {
				t.Fatalf("hook reference %q != response reference %q", got.d.Reference, body.Reference)
			}
		})
	}
}

// TestOnProblemDevModeCalledOnce pins that the hook also fires in dev mode,
// exactly once even on WriteUpstream's reason path.
func TestOnProblemDevModeCalledOnce(t *testing.T) {
	n := 0
	wr, err := New(Cfg{BaseURI: testBaseURI, OnProblem: func(*http.Request, Problem) { n++ }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	wr.WriteUpstream(httptest.NewRecorder(), req, stubUpstreamReason{
		stubUpstream: stubUpstream{status: http.StatusBadGateway, msg: "down"},
		reason:       "unreachable",
	}, "fallback")
	wr.WriteUpstream(httptest.NewRecorder(), req, stubUpstream{status: http.StatusBadGateway, msg: "down"}, "fallback")
	if n != 2 {
		t.Fatalf("OnProblem called %d times for two writes, want 2", n)
	}
}

// TestOnProblemMaskedKeepsFieldErrorsAndReason pins that a masked 422/502 stays
// diagnosable: the hook receives the validation field errors, and the upstream
// reason and error, even though ModeMasked strips them all from the body.
func TestOnProblemMaskedKeepsFieldErrorsAndReason(t *testing.T) {
	var got []Problem
	wr, err := New(Cfg{BaseURI: testBaseURI, Mode: ModeMasked, OnProblem: func(_ *http.Request, p Problem) { got = append(got, p) }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("WriteValidation", func(t *testing.T) {
		got = nil
		fields := []FieldError{{Pointer: "/items", Detail: "max 10"}, {Pointer: "/name", Detail: "required"}}
		w := httptest.NewRecorder()
		wr.WriteValidation(w, httptest.NewRequest(http.MethodPost, "/x", nil), "bad input", fields...)

		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v; body=%s", err, w.Body.String())
		}
		if _, ok := body["errors"]; ok {
			t.Fatalf("masked body must not include field errors, body=%s", w.Body.String())
		}
		if len(got) != 1 {
			t.Fatalf("OnProblem called %d times, want 1", len(got))
		}
		if !reflect.DeepEqual(got[0].Errors, fields) {
			t.Fatalf("OnProblem Errors = %+v, want %+v", got[0].Errors, fields)
		}
		if got[0].Reason != "" || got[0].Err != nil {
			t.Fatalf("validation problem must carry no reason/err, got %+v", got[0])
		}
	})

	t.Run("WriteUpstream", func(t *testing.T) {
		got = nil
		upErr := stubUpstreamReason{stubUpstream: stubUpstream{status: http.StatusBadGateway, msg: "down"}, reason: "unreachable"}
		w := httptest.NewRecorder()
		wr.WriteUpstream(w, httptest.NewRequest(http.MethodGet, "/x", nil), upErr, "fallback")

		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v; body=%s", err, w.Body.String())
		}
		if _, ok := body["reason"]; ok {
			t.Fatalf("masked body must not include reason, body=%s", w.Body.String())
		}
		if len(got) != 1 {
			t.Fatalf("OnProblem called %d times, want 1", len(got))
		}
		if got[0].Reason != "unreachable" {
			t.Fatalf("OnProblem Reason = %q, want %q", got[0].Reason, "unreachable")
		}
		if got[0].Err != upErr {
			t.Fatalf("OnProblem Err = %v, want the upstream error %v", got[0].Err, upErr)
		}
		if got[0].Errors != nil {
			t.Fatalf("upstream problem must carry no field errors, got %+v", got[0].Errors)
		}
	})
}

// TestStatusNormalised pins that a status outside 400-599 never reaches the
// client: Write maps it to 500 and WriteUpstream to 502 (no WriteHeader panic
// on 0, no problem body behind a success status), and the body's status and
// the OnProblem hook both see the normalised value.
func TestStatusNormalised(t *testing.T) {
	var hookStatus int
	wr, err := New(Cfg{BaseURI: testBaseURI, OnProblem: func(_ *http.Request, p Problem) { hookStatus = p.Status }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	write := func(status int) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) { wr.Write(w, r, status, SlugInternal, "boom") }
	}
	upstream := func(status int) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			wr.WriteUpstream(w, r, stubUpstream{status: status, msg: "down"}, "fallback")
		}
	}
	upstreamReason := func(status int) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			wr.WriteUpstream(w, r, stubUpstreamReason{stubUpstream: stubUpstream{status: status, msg: "down"}, reason: "x"}, "fallback")
		}
	}
	tests := []struct {
		name  string
		write func(http.ResponseWriter, *http.Request)
		want  int
	}{
		{"Write 0", write(0), http.StatusInternalServerError},
		{"Write 200", write(http.StatusOK), http.StatusInternalServerError},
		{"Write 302", write(http.StatusFound), http.StatusInternalServerError},
		{"Write 399", write(399), http.StatusInternalServerError},
		{"Write 600", write(600), http.StatusInternalServerError},
		{"Write 400", write(http.StatusBadRequest), http.StatusBadRequest},
		{"Write 404", write(http.StatusNotFound), http.StatusNotFound},
		{"Write 599", write(599), 599},
		{"WriteUpstream 0", upstream(0), http.StatusBadGateway},
		{"WriteUpstream 200", upstream(http.StatusOK), http.StatusBadGateway},
		{"WriteUpstream 600", upstream(600), http.StatusBadGateway},
		{"WriteUpstream 429", upstream(http.StatusTooManyRequests), http.StatusTooManyRequests},
		{"WriteUpstream 503", upstream(http.StatusServiceUnavailable), http.StatusServiceUnavailable},
		{"WriteUpstream reason 0", upstreamReason(0), http.StatusBadGateway},
		{"WriteUpstream reason 504", upstreamReason(http.StatusGatewayTimeout), http.StatusGatewayTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hookStatus = 0
			w := httptest.NewRecorder()
			tc.write(w, httptest.NewRequest(http.MethodGet, "/x", nil))

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			var body Details
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v; body=%s", err, w.Body.String())
			}
			if body.Status != tc.want {
				t.Fatalf("body status = %d, want %d", body.Status, tc.want)
			}
			if hookStatus != tc.want {
				t.Fatalf("OnProblem status = %d, want %d", hookStatus, tc.want)
			}
		})
	}
}

// TestBodyPinnedPerPath pins the exact wire body and the OnProblem Details of
// every write path in both modes, so the shared Details construction cannot
// drift between Write, WriteValidation, and WriteUpstream (with and without a
// dev-mode reason).
func TestBodyPinnedPerPath(t *testing.T) {
	const (
		path = "/api/v0/p"
		ref  = "ref-1"
	)
	tests := []struct {
		name     string
		mode     Mode
		write    func(wr *Writer, w http.ResponseWriter, r *http.Request)
		wantCode int
		wantBody string
		wantHook Problem
	}{
		{
			name: "Write dev", mode: ModeDev,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.Write(w, r, http.StatusConflict, SlugConflict, "already exists")
			},
			wantCode: http.StatusConflict,
			wantBody: `{"type":"https://example.test/probs/conflict","title":"Conflict","status":409,"detail":"already exists","instance":"/api/v0/p","reference":"ref-1"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/conflict", Title: "Conflict", Status: 409, Detail: "already exists", Instance: path, Reference: ref}},
		},
		{
			name: "Write masked", mode: ModeMasked,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.Write(w, r, http.StatusConflict, SlugConflict, "already exists")
			},
			wantCode: http.StatusConflict,
			wantBody: `{"type":"https://example.test/probs/error","title":"An error occurred","status":409,"instance":"/api/v0/p","reference":"ref-1"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/conflict", Title: "Conflict", Status: 409, Detail: "already exists", Instance: path, Reference: ref}},
		},
		{
			name: "WriteValidation dev", mode: ModeDev,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.WriteValidation(w, r, "bad input", FieldError{Pointer: "/qty", Detail: "too big"})
			},
			wantCode: http.StatusUnprocessableEntity,
			wantBody: `{"type":"https://example.test/probs/validation_error","title":"Validation error","status":422,"detail":"bad input","instance":"/api/v0/p","reference":"ref-1","errors":[{"pointer":"/qty","detail":"too big"}]}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/validation_error", Title: "Validation error", Status: 422, Detail: "bad input", Instance: path, Reference: ref}, Errors: []FieldError{{Pointer: "/qty", Detail: "too big"}}},
		},
		{
			name: "WriteValidation masked", mode: ModeMasked,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.WriteValidation(w, r, "bad input", FieldError{Pointer: "/qty", Detail: "too big"})
			},
			wantCode: http.StatusUnprocessableEntity,
			wantBody: `{"type":"https://example.test/probs/error","title":"An error occurred","status":422,"instance":"/api/v0/p","reference":"ref-1"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/validation_error", Title: "Validation error", Status: 422, Detail: "bad input", Instance: path, Reference: ref}, Errors: []FieldError{{Pointer: "/qty", Detail: "too big"}}},
		},
		{
			name: "WriteUpstream reason dev", mode: ModeDev,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.WriteUpstream(w, r, stubUpstreamReason{stubUpstream: stubUpstream{status: http.StatusGatewayTimeout, msg: "provider slow"}, reason: "timeout"}, "fallback")
			},
			wantCode: http.StatusGatewayTimeout,
			wantBody: `{"type":"https://example.test/probs/upstream_timeout","title":"Upstream timeout","status":504,"detail":"provider slow","instance":"/api/v0/p","reference":"ref-1","reason":"timeout"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/upstream_timeout", Title: "Upstream timeout", Status: 504, Detail: "provider slow", Instance: path, Reference: ref}, Reason: "timeout", Err: stubUpstreamReason{stubUpstream: stubUpstream{status: http.StatusGatewayTimeout, msg: "provider slow"}, reason: "timeout"}},
		},
		{
			name: "WriteUpstream reason masked", mode: ModeMasked,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.WriteUpstream(w, r, stubUpstreamReason{stubUpstream: stubUpstream{status: http.StatusGatewayTimeout, msg: "provider slow"}, reason: "timeout"}, "fallback")
			},
			wantCode: http.StatusGatewayTimeout,
			wantBody: `{"type":"https://example.test/probs/error","title":"An error occurred","status":504,"instance":"/api/v0/p","reference":"ref-1"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/upstream_timeout", Title: "Upstream timeout", Status: 504, Detail: "provider slow", Instance: path, Reference: ref}, Reason: "timeout", Err: stubUpstreamReason{stubUpstream: stubUpstream{status: http.StatusGatewayTimeout, msg: "provider slow"}, reason: "timeout"}},
		},
		{
			name: "WriteUpstream no reason dev", mode: ModeDev,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.WriteUpstream(w, r, stubUpstream{status: http.StatusTooManyRequests, msg: "slow down"}, "fallback")
			},
			wantCode: http.StatusTooManyRequests,
			wantBody: `{"type":"https://example.test/probs/upstream_rate_limited","title":"Upstream rate limited","status":429,"detail":"slow down","instance":"/api/v0/p","reference":"ref-1"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/upstream_rate_limited", Title: "Upstream rate limited", Status: 429, Detail: "slow down", Instance: path, Reference: ref}, Err: stubUpstream{status: http.StatusTooManyRequests, msg: "slow down"}},
		},
		{
			name: "WriteUpstream fallback masked", mode: ModeMasked,
			write: func(wr *Writer, w http.ResponseWriter, r *http.Request) {
				wr.WriteUpstream(w, r, errNotUpstream, "fallback")
			},
			wantCode: http.StatusBadGateway,
			wantBody: `{"type":"https://example.test/probs/error","title":"An error occurred","status":502,"instance":"/api/v0/p","reference":"ref-1"}`,
			wantHook: Problem{Details: Details{Type: testBaseURI + "/upstream_error", Title: "Upstream error", Status: 502, Detail: "fallback", Instance: path, Reference: ref}, Err: errNotUpstream},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hook []Problem
			wr, err := New(Cfg{
				BaseURI:   testBaseURI,
				Mode:      tc.mode,
				RequestID: func(*http.Request) string { return ref },
				OnProblem: func(_ *http.Request, p Problem) { hook = append(hook, p) },
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			w := httptest.NewRecorder()
			tc.write(wr, w, httptest.NewRequest(http.MethodGet, path, nil))

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", ct)
			}
			if got := w.Body.String(); got != tc.wantBody+"\n" {
				t.Fatalf("body =\n%s\nwant\n%s", got, tc.wantBody)
			}
			if len(hook) != 1 || !reflect.DeepEqual(hook[0], tc.wantHook) {
				t.Fatalf("OnProblem got %+v, want exactly [%+v]", hook, tc.wantHook)
			}
		})
	}
}
