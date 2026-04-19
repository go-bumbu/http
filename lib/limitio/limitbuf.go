package limitio

import (
	"bytes"
	"fmt"
)

// LimitedBuf is a wrapper around bytes.Buffer than only accepts writes up to certain max size
// it implements the io.ReadWriter interface
type LimitedBuf struct {
	bytes.Buffer
	MaxBytes  int
	curByte   int
	truncated bool
}

func (b *LimitedBuf) Reset() {
	b.Buffer.Reset()
	b.curByte = 0
	b.truncated = false
}

func (b *LimitedBuf) Write(p []byte) (n int, err error) {
	remaining := b.MaxBytes - b.curByte
	if remaining <= 0 {
		return 0, ErrBufferLimit
	}
	if len(p) > remaining {
		p = p[:remaining]
		n, err = b.Buffer.Write(p)
		b.curByte += n
		b.truncated = true
		if err != nil {
			return n, err
		}
		return n, ErrBufferLimit
	}
	n, err = b.Buffer.Write(p)
	b.curByte += n
	return n, err
}

func (b *LimitedBuf) Truncated() bool {
	return b.truncated
}

var ErrBufferLimit = fmt.Errorf("buffer write limit reached")
