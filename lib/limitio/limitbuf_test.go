package limitio_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/go-bumbu/http/lib/limitio"
)

func TestBuffer_Write(t *testing.T) {
	tests := []struct {
		name        string
		buffer      *limitio.LimitedBuf
		initialData []byte
		data        []byte
		expectedErr error
		expectedN   int
		expectedBuf string
	}{
		{
			name:        "Write within limit",
			buffer:      &limitio.LimitedBuf{MaxBytes: 10},
			data:        []byte("Hello"),
			expectedErr: nil,
			expectedN:   5,
			expectedBuf: "Hello",
		},
		{
			name:        "Write exceeds limit",
			buffer:      &limitio.LimitedBuf{MaxBytes: 5},
			data:        []byte("Hello, World!"),
			expectedErr: limitio.ErrBufferLimit,
			expectedN:   5,
			expectedBuf: "Hello",
		},
		{
			name:        "Write exactly at limit",
			buffer:      &limitio.LimitedBuf{MaxBytes: 5},
			data:        []byte("Hello"),
			expectedErr: nil,
			expectedN:   5,
			expectedBuf: "Hello",
		},
		{
			name:        "Write after limit exceeded",
			buffer:      &limitio.LimitedBuf{MaxBytes: 5},
			initialData: []byte("Hello"),
			data:        []byte("World"),
			expectedErr: limitio.ErrBufferLimit,
			expectedN:   0,
			expectedBuf: "Hello", // previous data
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the buffer before running each test
			tc.buffer.Reset()
			_, _ = tc.buffer.Write(tc.initialData)
			n, err := tc.buffer.Write(tc.data)

			if err != nil && err != tc.expectedErr {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}

			if n != tc.expectedN {
				t.Errorf("expected n = %d, got n = %d", tc.expectedN, n)
			}

			bufStr := tc.buffer.String()
			if bufStr != tc.expectedBuf {
				t.Errorf("expected buffer content %q, got %q", tc.expectedBuf, bufStr)
			}
		})
	}
}

func TestBuffer_Truncated(t *testing.T) {
	buf := &limitio.LimitedBuf{MaxBytes: 5}
	if buf.Truncated() {
		t.Error("expected Truncated() to be false initially")
	}
	_, _ = buf.Write([]byte("Hello, World!"))
	if !buf.Truncated() {
		t.Error("expected Truncated() to be true after exceeding limit")
	}
	buf.Reset()
	if buf.Truncated() {
		t.Error("expected Truncated() to be false after Reset()")
	}
}

func TestLimitWriter_Write(t *testing.T) {
	tests := []struct {
		name        string
		limit       int64
		data        []byte
		expectedN   int
		expectedErr error
		expectedBuf string
	}{
		{
			name:        "write within limit",
			limit:       10,
			data:        []byte("Hello"),
			expectedN:   5,
			expectedErr: nil,
			expectedBuf: "Hello",
		},
		{
			name:        "write exceeds limit",
			limit:       3,
			data:        []byte("Hello"),
			expectedN:   3,
			expectedErr: nil,
			expectedBuf: "Hel",
		},
		{
			name:        "write at zero limit",
			limit:       0,
			data:        []byte("Hello"),
			expectedN:   0,
			expectedErr: io.EOF,
			expectedBuf: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			lw := &limitio.LimitWriter{R: &buf, N: tc.limit}
			n, err := lw.Write(tc.data)
			if err != tc.expectedErr {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
			if n != tc.expectedN {
				t.Errorf("expected n = %d, got n = %d", tc.expectedN, n)
			}
			if buf.String() != tc.expectedBuf {
				t.Errorf("expected buffer content %q, got %q", tc.expectedBuf, buf.String())
			}
		})
	}
}

func TestLimitWriter_MultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitio.LimitWriter{R: &buf, N: 8}

	n, err := lw.Write([]byte("Hello"))
	if err != nil || n != 5 {
		t.Fatalf("first write: n=%d, err=%v", n, err)
	}

	n, err = lw.Write([]byte("World"))
	if err != nil || n != 3 {
		t.Fatalf("second write: expected n=3, got n=%d, err=%v", n, err)
	}

	n, err = lw.Write([]byte("!"))
	if err != io.EOF || n != 0 {
		t.Fatalf("third write: expected EOF, got n=%d, err=%v", n, err)
	}

	if buf.String() != "HelloWor" {
		t.Errorf("expected %q, got %q", "HelloWor", buf.String())
	}
}
