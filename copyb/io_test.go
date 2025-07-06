package copyb

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// testSource 是一个用于测试的字符串, 包含了各种字符.
const testSource = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()"

// TestCopy 测试 Copy 函数的基本功能.
func TestCopy(t *testing.T) {
	// 使用 strings.Reader 作为源.
	src := strings.NewReader(testSource)
	// 使用 bytes.Buffer 作为目标.
	dst := new(bytes.Buffer)

	// 调用我们实现的 Copy 函数.
	written, err := Copy(dst, src)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	// 验证写入的字节数是否正确.
	if written != int64(len(testSource)) {
		t.Errorf("Expected to write %d bytes, but wrote %d", len(testSource), written)
	}

	// 验证目标内容是否与源内容完全一致.
	if dst.String() != testSource {
		t.Errorf("Copied content does not match source. Got %q, want %q", dst.String(), testSource)
	}
}

// TestCopyBuffer 测试 CopyBuffer 函数的功能.
func TestCopyBuffer(t *testing.T) {
	src := strings.NewReader(testSource)
	dst := new(bytes.Buffer)
	// 提供一个自定义的缓冲区.
	buf := make([]byte, 16)

	written, err := CopyBuffer(dst, src, buf)
	if err != nil {
		t.Fatalf("CopyBuffer failed: %v", err)
	}

	if written != int64(len(testSource)) {
		t.Errorf("Expected to write %d bytes, but wrote %d", len(testSource), written)
	}

	if dst.String() != testSource {
		t.Errorf("Copied content does not match source. Got %q, want %q", dst.String(), testSource)
	}
}

// TestReadAll 测试 ReadAll 函数的基本功能.
func TestReadAll(t *testing.T) {
	src := strings.NewReader(testSource)
	// 调用我们实现的 ReadAll 函数.
	data, err := ReadAll(src)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	// 验证读取到的内容是否正确.
	if string(data) != testSource {
		t.Errorf("Read content does not match source. Got %q, want %q", string(data), testSource)
	}
}

// TestReadAllEmpty 测试 ReadAll 读取一个空源.
func TestReadAllEmpty(t *testing.T) {
	src := strings.NewReader("")
	data, err := ReadAll(src)
	if err != nil {
		t.Fatalf("ReadAll with empty source failed: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("Expected empty slice from empty source, but got %d bytes", len(data))
	}
}

// faultyReader 模拟一个在读取几次后会发生错误的 Reader.
type faultyReader struct {
	readsBeforeFailure int
	readCount          int
}

func (r *faultyReader) Read(p []byte) (n int, err error) {
	if r.readCount >= r.readsBeforeFailure {
		return 0, errors.New("a simulated error occurred")
	}
	// 每次只读取一个字节来方便计数.
	p[0] = 'a'
	r.readCount++
	return 1, nil
}

// TestCopyError 测试当源发生错误时 Copy 是否能正确处理.
func TestCopyError(t *testing.T) {
	src := &faultyReader{readsBeforeFailure: 3}
	dst := new(bytes.Buffer)

	written, err := Copy(dst, src)
	// 我们期望写入3个字节后发生错误.
	if written != 3 {
		t.Errorf("Expected to write 3 bytes before error, but wrote %d", written)
	}
	// 我们期望接收到模拟的错误.
	if err == nil || err.Error() != "a simulated error occurred" {
		t.Errorf("Expected a simulated error, but got: %v", err)
	}
}

// --- 基准测试 (Benchmarks) ---

// a large buffer for benchmarking
var largeSource = strings.Repeat("0123456789abcdef", 1024*4) // 64KB

// BenchmarkIoCopy 是标准库 io.Copy 的性能基准.
func BenchmarkIoCopy(b *testing.B) {
	src := strings.NewReader(largeSource)
	dst := io.Discard
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src.Seek(0, io.SeekStart)
		_, err := io.Copy(dst, src)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCopybCopy 是我们实现的 copyb.Copy 的性能基准.
func BenchmarkCopybCopy(b *testing.B) {
	src := strings.NewReader(largeSource)
	dst := io.Discard
	b.ResetTimer()
	b.ReportAllocs() // 报告内存分配情况, 这是关键指标.
	for i := 0; i < b.N; i++ {
		src.Seek(0, io.SeekStart)
		_, err := Copy(dst, src)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIoReadAll 是标准库 io.ReadAll 的性能基准.
func BenchmarkIoReadAll(b *testing.B) {
	src := strings.NewReader(largeSource)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		src.Seek(0, io.SeekStart)
		_, err := io.ReadAll(src)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCopybReadAll 是我们实现的 copyb.ReadAll 的性能基准.
func BenchmarkCopybReadAll(b *testing.B) {
	src := strings.NewReader(largeSource)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		src.Seek(0, io.SeekStart)
		_, err := ReadAll(src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
