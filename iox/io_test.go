package iox

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

// TestCopyN 测试 CopyN 函数的行为, 包括成功和提前遇到EOF的场景.
func TestCopyN(t *testing.T) {
	// 子测试1: 成功拷贝 n 个字节
	t.Run("SuccessCase", func(t *testing.T) {
		sourceString := "0123456789" // 源数据 (10字节)
		src := strings.NewReader(sourceString)
		dst := new(bytes.Buffer)
		n := int64(5) // 期望拷贝的字节数

		written, err := CopyN(dst, src, n)

		// 验证错误应为 nil
		if err != nil {
			t.Fatalf("expected no error, but got: %v", err)
		}
		// 验证写入的字节数
		if written != n {
			t.Errorf("expected written bytes to be %d, but got %d", n, written)
		}
		// 验证拷贝的内容
		expectedContent := "01234"
		if dst.String() != expectedContent {
			t.Errorf("expected copied content to be %q, but got %q", expectedContent, dst.String())
		}
	})

	// 子测试2: 源数据不足 n 个字节, 提前遇到 EOF
	t.Run("EarlyEOFCase", func(t *testing.T) {
		sourceString := "01234" // 源数据 (5字节)
		src := strings.NewReader(sourceString)
		dst := new(bytes.Buffer)
		n := int64(10) // 期望拷贝10字节, 但源只有5字节

		written, err := CopyN(dst, src, n)

		// 验证错误必须是 io.EOF
		if err != io.EOF {
			t.Fatalf("expected io.EOF, but got: %v", err)
		}
		// 验证写入的字节数等于源的实际长度
		expectedWritten := int64(len(sourceString))
		if written != expectedWritten {
			t.Errorf("expected written bytes to be %d, but got %d", expectedWritten, written)
		}
		// 验证拷贝的内容是整个源
		if dst.String() != sourceString {
			t.Errorf("expected copied content to be %q, but got %q", sourceString, dst.String())
		}
	})
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
