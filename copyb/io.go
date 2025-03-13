package copyb

import (
	"errors"
	"io"

	"github.com/valyala/bytebufferpool" // Import bytebufferpool
)

var EOF = errors.New("EOF")

type Writer interface {
	Write(p []byte) (n int, err error)
}

type Reader interface {
	Read(p []byte) (n int, err error)
}

type WriterTo interface {
	WriteTo(w Writer) (n int64, err error)
}

type ReaderFrom interface {
	ReadFrom(r Reader) (n int64, err error)
}

type LimitedReader struct {
	R Reader // underlying reader
	N int64  // max bytes remaining
}

func (l *LimitedReader) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, EOF
	}
	if int64(len(p)) > l.N {
		p = p[0:l.N]
	}
	n, err = l.R.Read(p)
	l.N -= int64(n)
	return
}

// CopyBuffer is identical to Copy except that it stages through the
// provided buffer (if one is required) rather than allocating a
// temporary one. If buf is nil, one is allocated; otherwise if it has
// zero length, CopyBuffer panics.
//
// If either src implements [WriterTo] or dst implements [ReaderFrom],
// buf will not be used to perform the copy.
func CopyBuffer(dst Writer, src Reader, buf []byte) (written int64, err error) {
	if buf != nil && len(buf) == 0 {
		panic("empty buffer in CopyBuffer")
	}
	return copyBuffer(dst, src, buf)
}

// copyBuffer is the actual implementation of Copy and CopyBuffer.
// if buf is nil, one is allocated from bytebufferpool.
func copyBuffer(dst Writer, src Reader, buf []byte) (written int64, err error) {
	// If the reader has a WriteTo method, use it to do the copy.
	// Avoids an allocation and a copy.
	if wt, ok := src.(WriterTo); ok {
		return wt.WriteTo(dst)
	}
	// Similarly, if the writer has a ReadFrom method, use it to do the copy.
	if rf, ok := dst.(ReaderFrom); ok {
		return rf.ReadFrom(src)
	}

	var bufWrapper *bytebufferpool.ByteBuffer // Declare ByteBuffer wrapper

	if buf == nil {
		size := 32 * 1024
		if l, ok := src.(*LimitedReader); ok && int64(size) > l.N {
			if l.N < 1 {
				size = 1
			} else {
				size = int(l.N)
			}
		}
		bufWrapper = bytebufferpool.Get() // Get buffer from pool
		buf = bufWrapper.B                // Use the underlying byte slice
		if cap(buf) < size {              // Ensure capacity is sufficient, though bytebufferpool usually handles this
			buf = make([]byte, size) // Fallback to make if pool buffer is too small, though unlikely
		} else {
			buf = buf[:size] // Limit buffer to 'size' if pool buffer is larger
		}
		defer bytebufferpool.Put(bufWrapper) // Return buffer to pool when done
	}

	var er error
	var nr int
	for {
		nr, er = src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = ErrShortWrite
				break
			}
		}
		if er != nil {
			if err == EOF || err == io.EOF { // 当遇到 EOF 时，正常退出循环，不返回错误
				err = nil
				break
			} else {
				err = er
				break
			}
		}
	}
	if err != nil {
		if err == EOF || err == io.EOF { // 当遇到 EOF 时，正常退出循环，不返回错误
			err = nil
			return written, nil
		} else {
			return written, err
		}
	}

	return written, nil
}

// errInvalidWrite means that a write returned an impossible count.
var errInvalidWrite = errors.New("invalid write result")

// ErrShortWrite means that a write accepted fewer bytes than requested
// but failed to return an explicit error.
var ErrShortWrite = errors.New("short write")

func Copy(dst Writer, src Reader) (written int64, err error) {
	return CopyBuffer(dst, src, nil)
}
