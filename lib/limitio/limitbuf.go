package limitio

import (
	"bytes"
	"fmt"
)

// LimitedBuf is a wrapper around bytes.Buffer than only accepts writes up to certain max size
// it implements the io.ReadWriter interface
type LimitedBuf struct {
	bytes.Buffer
	MaxBytes int
	curByte  int
}

func (b *LimitedBuf) Reset() {
	b.Buffer.Reset()
	b.curByte = 0
}

func (b *LimitedBuf) Write(p []byte) (n int, err error) {
	if len(p)+b.curByte > b.MaxBytes {
		return 0, BufferLimitErr
	}
	n, err = b.Buffer.Write(p)
	b.curByte += n
	return n, err
}

var BufferLimitErr = fmt.Errorf("buffer write limit reached")
