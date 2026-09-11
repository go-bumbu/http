package middleware

import "bytes"

// limitBuf collects up to max bytes of a response body for logging. It behaves
// like a capped io.Discard: writes past the cap are dropped rather than stored,
// and Write always reports the full length consumed with a nil error, so a
// handler writing a large body is never blocked by the cap. Whether anything was
// dropped is observable only through Truncated.
//
// It composes bytes.Buffer (rather than embedding it) so that no promoted method
// can write past the cap. It is an internal logging sink, not a general-purpose
// io.Writer, and is not safe for concurrent use.
type limitBuf struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

// newLimitBuf returns a limitBuf that retains at most max bytes.
func newLimitBuf(max int) *limitBuf {
	return &limitBuf{max: max}
}

// Write stores as much of p as fits under the cap and discards the rest, setting
// truncated when any byte is dropped. It always returns (len(p), nil).
func (b *limitBuf) Write(p []byte) (int, error) {
	remaining := b.max - b.buf.Len()
	switch {
	case remaining <= 0:
		if len(p) > 0 {
			b.truncated = true
		}
	case len(p) > remaining:
		b.buf.Write(p[:remaining])
		b.truncated = true
	default:
		b.buf.Write(p)
	}
	return len(p), nil
}

// Read drains buffered bytes so io.ReadAll can consume them for logging.
func (b *limitBuf) Read(p []byte) (int, error) {
	return b.buf.Read(p)
}

// Bytes returns the buffered bytes without consuming them.
func (b *limitBuf) Bytes() []byte {
	return b.buf.Bytes()
}

// Truncated reports whether any write was dropped because the cap was reached.
func (b *limitBuf) Truncated() bool {
	return b.truncated
}

// String returns the buffered bytes as a string.
func (b *limitBuf) String() string {
	return b.buf.String()
}

// Len returns the number of bytes buffered.
func (b *limitBuf) Len() int {
	return b.buf.Len()
}
