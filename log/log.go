// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package log implements a simple logging package. It defines a type, [Logger],
// with methods for formatting output. It also has a predefined 'standard'
// Logger accessible through helper functions Print[f|ln], Fatal[f|ln], and
// Panic[f|ln], which are easier to use than creating a Logger manually.
// That logger writes to standard error and prints the date and time
// of each logged message.
// Every log message is output on a separate line: if the message being
// printed does not end in a newline, the logger will add one.
// The Fatal functions call [os.Exit](1) after writing the log message.
// The Panic functions call panic after writing the log message.
package log

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Ldate         = 1 << iota     // the date in the local time zone: 2009/01/23
	Ltime                         // the time in the local time zone: 01:23:23
	Lmicroseconds                 // microsecond resolution: 01:23:23.123123.  assumes Ltime.
	Llongfile                     // full file name and line number: /a/b/c/d.go:23
	Lshortfile                    // final file name element and line number: d.go:23. overrides Llongfile
	LUTC                          // if Ldate or Ltime is set, use UTC rather than the local time zone
	Lmsgprefix                    // move the "prefix" from the beginning of the line to before the message
	LstdFlags     = Ldate | Ltime // initial values for the standard logger
)

type Logger struct {
	outMu       sync.Mutex
	out         io.Writer
	prefix      atomic.Pointer[string]
	flag        atomic.Int32
	isDiscard   atomic.Bool
	asyncWriter *asyncWriter // 新增异步写入器
	asyncMode   atomic.Bool  // 异步模式标志
}

// 添加异步结构体
type asyncWriter struct {
	logger    *Logger
	logChan   chan *[]byte // MODIFIED: Store pointers to byte slices
	wg        sync.WaitGroup
	closeChan chan struct{}
}

// 创建异步写入器
func newAsyncWriter(l *Logger, bufferSize int) *asyncWriter {
	aw := &asyncWriter{
		logger:    l,
		logChan:   make(chan *[]byte, bufferSize), // MODIFIED: Channel of pointers
		closeChan: make(chan struct{}),
	}
	aw.wg.Add(1)
	go aw.process()
	return aw
}

// 异步处理协程
func (aw *asyncWriter) process() {
	defer aw.wg.Done()
	for {
		select {
		case entryBufPtr := <-aw.logChan: // entryBufPtr is *[]byte
			aw.logger.outMu.Lock()
			aw.logger.out.Write(*entryBufPtr)
			aw.logger.outMu.Unlock()
			putBuffer(entryBufPtr) // MODIFIED: Return buffer to pool after writing
		case <-aw.closeChan:
			// 关闭前清空通道
			// Drain any remaining messages from the channel
			// This loop ensures that we process messages that might have been added
			// while we were processing the closeChan signal.
			for {
				select {
				case entryBufPtr := <-aw.logChan:
					aw.logger.outMu.Lock() // ADDED: Lock for consistency
					aw.logger.out.Write(*entryBufPtr)
					aw.logger.outMu.Unlock() // ADDED: Unlock
					putBuffer(entryBufPtr)   // MODIFIED: Return buffer to pool
				default:
					// logChan is empty, we can return
					return
				}
			}
		}
	}
}

// 启用异步模式（需在首次日志调用前设置）
func (l *Logger) SetAsync(bufferSize int) {
	if l.asyncMode.CompareAndSwap(false, true) {
		l.asyncWriter = newAsyncWriter(l, bufferSize)
	}
}

// 安全关闭异步写入器
func (l *Logger) Close() error {
	if l.asyncMode.Load() { // Check if it was ever in async mode
		// Attempt to set asyncMode to false. If it was already false, do nothing.
		// This helps prevent new async dispatches if Close is called multiple times
		// or if it was never truly async.
		swapped := l.asyncMode.CompareAndSwap(true, false)
		if swapped { // Only close if we were the ones to turn off async mode
			close(l.asyncWriter.closeChan)
			l.asyncWriter.wg.Wait()
		}
	}
	return nil
}

func New(out io.Writer, prefix string, flag int) *Logger {
	l := new(Logger)
	l.SetOutput(out)
	l.SetPrefix(prefix)
	l.SetFlags(flag)
	return l
}

func (l *Logger) SetOutput(w io.Writer) {
	l.outMu.Lock()
	defer l.outMu.Unlock()
	l.out = w
	l.isDiscard.Store(w == io.Discard)
}

var std = New(os.Stderr, "", LstdFlags)

func Default() *Logger { return std }

func formatHeader(buf *[]byte, t time.Time, prefix string, flag int, file string, line int) {
	if flag&Lmsgprefix == 0 {
		*buf = append(*buf, prefix...)
	}
	if flag&(Ldate|Ltime|Lmicroseconds) != 0 {
		if flag&LUTC != 0 {
			t = t.UTC()
		}
		if flag&Ldate != 0 {
			year, month, day := t.Date()
			*buf = append(*buf,
				byte('0'+(year/1000)%10),
				byte('0'+(year/100)%10),
				byte('0'+(year/10)%10),
				byte('0'+year%10),
				'/',
				byte('0'+(int(month)/10)),
				byte('0'+(int(month)%10)),
				'/',
				byte('0'+(day/10)),
				byte('0'+(day%10)),
				' ',
			)
		}
		if flag&(Ltime|Lmicroseconds) != 0 {
			hour, min, sec := t.Clock()
			*buf = append(*buf,
				byte('0'+(hour/10)),
				byte('0'+(hour%10)),
				':',
				byte('0'+(min/10)),
				byte('0'+(min%10)),
				':',
				byte('0'+(sec/10)),
				byte('0'+(sec%10)),
			)
			if flag&Lmicroseconds != 0 {
				micro := t.Nanosecond() / 1e3
				*buf = append(*buf,
					'.',
					byte('0'+(micro/100000)%10),
					byte('0'+(micro/10000)%10),
					byte('0'+(micro/1000)%10),
					byte('0'+(micro/100)%10),
					byte('0'+(micro/10)%10),
					byte('0'+micro%10),
				)
			}
			*buf = append(*buf, ' ')
		}
	}
	if flag&(Lshortfile|Llongfile) != 0 {
		if flag&Lshortfile != 0 {
			for i := len(file) - 1; i > 0; i-- {
				if file[i] == '/' {
					file = file[i+1:]
					break
				}
			}
		}
		*buf = append(*buf, file...)
		*buf = append(*buf, ':')
		itoa(buf, line, -1)
		*buf = append(*buf, ": "...)
	}
	if flag&Lmsgprefix != 0 {
		*buf = append(*buf, prefix...)
	}
}

var bufferPool = sync.Pool{New: func() any { return new([]byte) }}

func getBuffer() *[]byte {
	p := bufferPool.Get().(*[]byte)
	*p = (*p)[:0]
	return p
}

func putBuffer(p *[]byte) {
	if p == nil { // Robustness
		return
	}
	// Shrink buffer if it's too large, to avoid holding onto large buffers indefinitely.
	// For example, if cap(*p) > 256 && cap(*p) > 2 * len(*p) {
	//    *p = make([]byte, len(*p)) // Create a new slice with just enough capacity.
	// } else if cap(*p) > 64<<10 { // Original condition, might be too aggressive if large logs are common
	if cap(*p) > 64<<10 { // Keep original logic for pool max capacity
		*p = nil // Effectively discards it for GC, pool will create new one next time
	}
	bufferPool.Put(p)
}

func (l *Logger) output(pc uintptr, calldepth int, appendOutput func([]byte) []byte) error {
	if l.isDiscard.Load() {
		return nil
	}

	var now time.Time
	flag := l.Flags()
	if flag&(Ldate|Ltime|Lmicroseconds) != 0 {
		now = time.Now() // Lshortfile, Llongfile, LUTC uses later
		if flag&LUTC != 0 {
			now = now.UTC()
		}
	}

	prefix := l.Prefix()
	var file string
	var line int
	if flag&(Lshortfile|Llongfile) != 0 {
		// releaseLock purely for runtime.Callers,
		// as it may malloc. It is safe to do after CAS,
		// as the critical variables will not be changed.
		// l.outMu.Unlock() // Original log has this, but we are not holding it yet.
		var ok bool
		if pc == 0 {
			_, file, line, ok = runtime.Caller(calldepth)
		} else {
			// This path is taken by Fatal and Panic via internal.DefaultOutput,
			// which we are not using directly here, but good to keep consistent.
			frames := runtime.CallersFrames([]uintptr{pc})
			frame, _ := frames.Next()
			file = frame.File
			line = frame.Line
			ok = file != ""
		}
		if !ok {
			file = "???"
			line = 0
		}
		// l.outMu.Lock() // Re-acquire if unlocked above
	}

	buf := getBuffer()
	// No `defer putBuffer(buf)` here anymore. It's conditional.

	formatHeader(buf, now, prefix, flag, file, line)
	*buf = appendOutput(*buf)
	if len(*buf) == 0 || (*buf)[len(*buf)-1] != '\n' {
		*buf = append(*buf, '\n')
	}

	var err error
	if l.asyncMode.Load() && l.asyncWriter != nil { // Check asyncWriter != nil for safety during setup/teardown
		// Send the pointer to the buffer to the async writer.
		// The async writer is now responsible for calling putBuffer.
		select {
		case l.asyncWriter.logChan <- buf:
			// Buffer ownership transferred to asyncWriter. It will call putBuffer.
			// Do not call putBuffer(buf) here.
			return nil
		default:
			// Channel full or async writer not ready, fallback to synchronous write.
			// We (this goroutine) still own buf, so we must putBuffer it.
			defer putBuffer(buf) // Ensure buffer is returned on this path
			l.outMu.Lock()
			_, err = l.out.Write(*buf)
			l.outMu.Unlock()
		}
	} else {
		// Synchronous mode or async not fully initialized. We own buf.
		defer putBuffer(buf) // Ensure buffer is returned on this path
		l.outMu.Lock()
		_, err = l.out.Write(*buf)
		l.outMu.Unlock()
	}
	return err
}

// Cheap integer to fixed-width decimal ASCII. Give a negative width to avoid zero-padding.
func itoa(buf *[]byte, i int, wid int) {
	// Assemble decimal in reverse order.
	var b [20]byte
	bp := len(b) - 1
	for i >= 10 || wid > 1 {
		wid--
		q := i / 10
		b[bp] = byte('0' + i - q*10)
		bp--
		i = q
	}
	// i < 10
	b[bp] = byte('0' + i)
	*buf = append(*buf, b[bp:]...)
}

func (l *Logger) Output(calldepth int, s string) error {
	calldepth++ // +1 for this frame.
	// Frame depth: 0: Output, 1: (Print|Printf|Println|Fatal|...), 2: caller of (Print|...)
	return l.output(0, calldepth, func(b []byte) []byte {
		return append(b, s...)
	})
}

func (l *Logger) Print(v ...any) {
	l.output(0, 2, func(b []byte) []byte {
		return fmt.Append(b, v...)
	})
}

func (l *Logger) Printf(format string, v ...any) {
	l.output(0, 2, func(b []byte) []byte {
		return fmt.Appendf(b, format, v...)
	})
}

func (l *Logger) Println(v ...any) {
	l.output(0, 2, func(b []byte) []byte {
		return fmt.Appendln(b, v...)
	})
}

func (l *Logger) Fatal(v ...any) {
	s := fmt.Sprint(v...)
	l.output(0, 2, func(b []byte) []byte { // Use output for consistent formatting and async handling
		return append(b, s...)
	})
	os.Exit(1)
}

func (l *Logger) Fatalf(format string, v ...any) {
	s := fmt.Sprintf(format, v...)
	l.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	os.Exit(1)
}

func (l *Logger) Fatalln(v ...any) {
	s := fmt.Sprintln(v...)
	l.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	os.Exit(1)
}

func (l *Logger) Panic(v ...any) {
	s := fmt.Sprint(v...)
	l.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	panic(s)
}

func (l *Logger) Panicf(format string, v ...any) {
	s := fmt.Sprintf(format, v...)
	l.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	panic(s)
}

func (l *Logger) Panicln(v ...any) {
	s := fmt.Sprintln(v...)
	l.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	panic(s)
}

func (l *Logger) Flags() int {
	return int(l.flag.Load())
}

func (l *Logger) SetFlags(flag int) {
	l.flag.Store(int32(flag))
}

func (l *Logger) Prefix() string {
	if p := l.prefix.Load(); p != nil {
		return *p
	}
	return ""
}

func (l *Logger) SetPrefix(prefix string) {
	l.prefix.Store(&prefix)
}

func (l *Logger) Writer() io.Writer {
	l.outMu.Lock()
	defer l.outMu.Unlock()
	return l.out
}

func SetOutput(w io.Writer) {
	std.SetOutput(w)
}

func Flags() int {
	return std.Flags()
}

func SetFlags(flag int) {
	std.SetFlags(flag)
}

func Prefix() string {
	return std.Prefix()
}

func SetPrefix(prefix string) {
	std.SetPrefix(prefix)
}

func Writer() io.Writer {
	return std.Writer()
}

func Print(v ...any) {
	std.output(0, 2, func(b []byte) []byte {
		return fmt.Append(b, v...)
	})
}

func Printf(format string, v ...any) {
	std.output(0, 2, func(b []byte) []byte {
		return fmt.Appendf(b, format, v...)
	})
}

func Println(v ...any) {
	std.output(0, 2, func(b []byte) []byte {
		return fmt.Appendln(b, v...)
	})
}

func Fatal(v ...any) {
	s := fmt.Sprint(v...)
	std.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	os.Exit(1)
}

func Fatalf(format string, v ...any) {
	s := fmt.Sprintf(format, v...)
	std.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	os.Exit(1)
}

func Fatalln(v ...any) {
	s := fmt.Sprintln(v...)
	std.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	os.Exit(1)
}

func Panic(v ...any) {
	s := fmt.Sprint(v...)
	std.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	panic(s)
}

func Panicf(format string, v ...any) {
	s := fmt.Sprintf(format, v...)
	std.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	panic(s)
}

func Panicln(v ...any) {
	s := fmt.Sprintln(v...)
	std.output(0, 2, func(b []byte) []byte {
		return append(b, s...)
	})
	panic(s)
}

func Output(calldepth int, s string) error {
	return std.Output(calldepth+1, s) // +1 for this frame.
}
