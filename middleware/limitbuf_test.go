package middleware

import (
	"io"
	"strings"
	"testing"
)

func TestLimitBuf_Write(t *testing.T) {
	tests := []struct {
		name          string
		max           int
		writes        []string
		wantContent   string
		wantTruncated bool
	}{
		{name: "within limit", max: 10, writes: []string{"Hello"}, wantContent: "Hello", wantTruncated: false},
		{name: "exactly at limit", max: 5, writes: []string{"Hello"}, wantContent: "Hello", wantTruncated: false},
		{name: "single write exceeds limit", max: 5, writes: []string{"Hello, World!"}, wantContent: "Hello", wantTruncated: true},
		{name: "write after buffer already full", max: 5, writes: []string{"Hello", "World"}, wantContent: "Hello", wantTruncated: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newLimitBuf(tc.max)
			for _, w := range tc.writes {
				n, err := b.Write([]byte(w))
				if err != nil {
					t.Fatalf("Write(%q) returned error: %v", w, err)
				}
				if n != len(w) {
					t.Errorf("Write(%q) returned n = %d, want %d", w, n, len(w))
				}
			}
			if got := string(b.Bytes()); got != tc.wantContent {
				t.Errorf("buffer content = %q, want %q", got, tc.wantContent)
			}
			if b.Truncated() != tc.wantTruncated {
				t.Errorf("Truncated() = %v, want %v", b.Truncated(), tc.wantTruncated)
			}
		})
	}
}

func TestLimitBuf_EmptyWriteWhenFullIsNoTruncate(t *testing.T) {
	b := newLimitBuf(5)
	if _, err := b.Write([]byte("Hello")); err != nil {
		t.Fatalf("fill write: %v", err)
	}
	n, err := b.Write(nil)
	if err != nil || n != 0 {
		t.Fatalf("empty write when full: got n=%d, err=%v; want n=0, err=nil", n, err)
	}
	if b.Truncated() {
		t.Error("empty write when full must not set Truncated()")
	}
}

func TestLimitBuf_ReadBack(t *testing.T) {
	b := newLimitBuf(20)
	if _, err := b.Write([]byte("log body")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	if string(got) != "log body" {
		t.Errorf("io.ReadAll = %q, want %q", got, "log body")
	}
}

// TestLimitBuf_CopyRespectsCap guards the bug that motivated dropping the embedded
// bytes.Buffer: the old type let io.Copy bypass the cap via the promoted ReadFrom.
// The source is wrapped to hide its io.WriterTo, so io.Copy cannot take the
// src.WriteTo shortcut and must look for dst.ReadFrom — which limitBuf deliberately
// does not implement, forcing the capped Write loop. If embedding were reintroduced
// (promoting ReadFrom), this copy would bypass the cap and the assertions would fail.
func TestLimitBuf_CopyRespectsCap(t *testing.T) {
	b := newLimitBuf(5)
	n, err := io.Copy(b, struct{ io.Reader }{strings.NewReader("Hello, World!")})
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if n != int64(len("Hello, World!")) {
		t.Errorf("io.Copy reported n = %d, want %d", n, len("Hello, World!"))
	}
	if got := string(b.Bytes()); got != "Hello" {
		t.Errorf("buffer content = %q, want %q (cap must hold through io.Copy)", got, "Hello")
	}
	if !b.Truncated() {
		t.Error("Truncated() = false, want true after copying past the cap")
	}
}
