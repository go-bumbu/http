package limitio_test

import (
	"github.com/go-bumbu/http/lib/limitio"
	"testing"
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
			expectedErr: limitio.BufferLimitErr,
			expectedN:   0,
			expectedBuf: "",
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
			expectedErr: limitio.BufferLimitErr,
			expectedN:   0,
			expectedBuf: "Hello", // previous data
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the buffer before running each test
			tc.buffer.Reset()
			tc.buffer.Write(tc.initialData)
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
