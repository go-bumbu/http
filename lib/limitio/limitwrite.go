package limitio

import "io"

type LimitWriter struct {
	R io.Writer // underlying writer
	N int64     // max bytes remaining
}

func (l *LimitWriter) Write(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.N {
		p = p[0:l.N]
	}
	n, err = l.R.Write(p)
	l.N -= int64(n)
	return
}
