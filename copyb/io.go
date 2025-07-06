package copyb

import (
	"errors"
	"io"

	"github.com/valyala/bytebufferpool"
)

// errInvalidWrite 表示一次写入返回了一个不可能的计数值.
// 这是对 io 包中未导出的同名错误的本地实现, 以保持兼容性.
var errInvalidWrite = errors.New("invalid write result")

// copyBuffer 是 Copy 和 CopyBuffer 的核心实现.
// 如果 buf 为 nil, 则从 bytebufferpool 中获取一个进行池化操作.
func copyBuffer(dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	// 快速路径: 如果 src 实现了 io.WriterTo 接口, 直接使用它进行拷贝,
	// 这样可以避免分配和使用中间缓冲区, 效率最高.
	if wt, ok := src.(io.WriterTo); ok {
		return wt.WriteTo(dst)
	}

	// 快速路径: 同样, 如果 dst 实现了 io.ReaderFrom 接口, 也直接使用它进行拷贝.
	if rf, ok := dst.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}

	// 如果外部没有提供缓冲区, 我们从池中获取一个.
	if buf == nil {
		// 定义一个进行高效I/O操作的理想缓冲区大小.
		const defaultBufSize = 32 * 1024

		// 从池中获取一个 ByteBuffer 对象.
		bb := bytebufferpool.Get()
		defer bytebufferpool.Put(bb)

		// 检查池中获取的缓冲区容量.
		// 如果容量小于我们的理想大小, 则进行一次“投资性”分配,
		// 将其内部的切片替换为一个更大的切片.
		// 这可以“升级”池中的小缓冲区, 使未来的复用更高效.
		if cap(bb.B) < defaultBufSize {
			bb.B = make([]byte, defaultBufSize)
		}

		buf = bb.B[:defaultBufSize]
	}

	// 核心拷贝循环, 逻辑与标准库 io.Copy 保持一致, 保证健壮性.
	for {
		// 从源读取数据到缓冲区.
		nr, er := src.Read(buf)
		if nr > 0 {
			// 将读取到的数据写入目标.
			nw, ew := dst.Write(buf[0:nr])
			// 检查写入的字节数是否合理.
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			// 如果写入时发生错误, 立即返回该错误.
			if ew != nil {
				err = ew
				break
			}
			// 如果写入的字节数少于读取的字节数, 说明发生了短写入, 返回错误.
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		// 如果读取时发生错误.
		if er != nil {
			// 如果错误是 EOF, 这是正常的流结束信号, 不应作为错误返回.
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}

// CopyBuffer 类似于 io.CopyBuffer, 但在需要时会使用 bytebufferpool 来分配缓冲区.
// 如果 buf 为 nil, 将从池中获取一个; 如果 buf 长度为0, 会导致 panic.
// 如果 src 实现了 io.WriterTo 或 dst 实现了 io.ReaderFrom, 将不会使用 buf.
func CopyBuffer(dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	if buf != nil && len(buf) == 0 {
		panic("empty buffer in CopyBuffer")
	}
	return copyBuffer(dst, src, buf)
}

// Copy 类似于 io.Copy, 但内部使用 bytebufferpool 来获取临时缓冲区, 以减少内存分配.
// 这在需要高性能、高并发拷贝大量数据的场景下非常有用.
func Copy(dst io.Writer, src io.Reader) (written int64, err error) {
	return copyBuffer(dst, src, nil)
}

// ReadAll 从 Reader r 中读取所有数据直到 EOF, 并返回读取的数据.
// 它使用 bytebufferpool 来获取一个大的临时缓冲区, 避免了标准库 io.ReadAll
// 在读取过程中可能发生的多次内存分配和拷贝, 从而显著降低GC压力.
//
// 函数会返回一个全新的数据切片副本, 池化的缓冲区会被安全地回收.
func ReadAll(r io.Reader) ([]byte, error) {
	// 从池中获取一个 ByteBuffer.
	bb := bytebufferpool.Get()
	// 确保函数退出时将 ByteBuffer 归还到池中.
	defer bytebufferpool.Put(bb)

	// 使用 ByteBuffer 实现的 io.ReaderFrom, 高效地读取所有数据.
	// ReadFrom 内部会自动处理缓冲区的扩容.
	_, err := bb.ReadFrom(r)
	if err != nil {
		return nil, err
	}

	// 必须创建并返回数据的副本, 因为 bb 即将被回收并可能被覆盖.
	// 这是保证数据安全的关键步骤.
	b := make([]byte, len(bb.B))
	copy(b, bb.B)
	return b, nil
}
