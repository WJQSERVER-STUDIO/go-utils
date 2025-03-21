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
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Ldate = 1 << iota
	Ltime
	Lmicroseconds
	Llongfile
	Lshortfile
	LUTC
	Lmsgprefix
	LstdFlags = Ldate | Ltime
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
	logChan   chan []byte
	wg        sync.WaitGroup
	closeChan chan struct{}
}

// 创建异步写入器
func newAsyncWriter(l *Logger, bufferSize int) *asyncWriter {
	aw := &asyncWriter{
		logger:    l,
		logChan:   make(chan []byte, bufferSize),
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
		case entry := <-aw.logChan:
			aw.logger.outMu.Lock()
			aw.logger.out.Write(entry)
			aw.logger.outMu.Unlock()
		case <-aw.closeChan:
			// 关闭前清空通道
			for len(aw.logChan) > 0 {
				entry := <-aw.logChan
				aw.logger.out.Write(entry)
			}
			return
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
	if l.asyncMode.CompareAndSwap(true, false) {
		close(l.asyncWriter.closeChan)
		l.asyncWriter.wg.Wait()
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
		*buf = strconv.AppendInt(*buf, int64(line), 10)
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
	if cap(*p) > 64<<10 {
		*p = nil
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
		now = time.Now()
		if flag&LUTC != 0 {
			now = now.UTC()
		}
	}

	prefix := l.Prefix()
	var file string
	var line int
	if flag&(Lshortfile|Llongfile) != 0 {
		if pc == 0 {
			_, file, line, _ = runtime.Caller(calldepth)
		} else {
			f, _ := runtime.CallersFrames([]uintptr{pc}).Next()
			file = f.File
		}
		if file == "" {
			file = "???"
		}
	}

	buf := getBuffer()
	defer putBuffer(buf)
	formatHeader(buf, now, prefix, flag, file, line)
	*buf = appendOutput(*buf)
	if len(*buf) == 0 || (*buf)[len(*buf)-1] != '\n' {
		*buf = append(*buf, '\n')
	}

	if l.asyncMode.Load() {
		select {
		case l.asyncWriter.logChan <- *buf:
		default:
			// 通道满时降级为同步写入
			l.outMu.Lock()
			defer l.outMu.Unlock()
			_, err := l.out.Write(*buf)
			if err != nil {
				return err
			}
		}
		return nil
	} else {
		// 原有同步写入逻辑
		l.outMu.Lock()
		defer l.outMu.Unlock()
		_, err := l.out.Write(*buf)
		return err
	}

	/*
		l.outMu.Lock()
		_, err := l.out.Write(*buf)
		l.outMu.Unlock()
		return err
	*/
}

/*
func init() {
	internal.DefaultOutput = func(pc uintptr, data []byte) error {
		return std.output(pc, 0, func(buf []byte) []byte {
			return append(buf, data...)
		})
	}
}
*/

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

// Output writes the output for a logging event. The string s contains
// the text to print after the prefix specified by the flags of the
// Logger. A newline is appended if the last character of s is not
// already a newline. Calldepth is used to recover the PC and is
// provided for generality, although at the moment on all pre-defined
// paths it will be 2.
func (l *Logger) Output(calldepth int, s string) error {
	calldepth++ // +1 for this frame.
	return l.output(0, calldepth, func(b []byte) []byte {
		return append(b, s...)
	})
}

/*
func init() {
	internal.DefaultOutput = func(pc uintptr, data []byte) error {
		return std.output(pc, 0, func(buf []byte) []byte {
			return append(buf, data...)
		})
	}
}
*/

// Print calls l.Output to print to the logger.
// Arguments are handled in the manner of [fmt.Print].
func (l *Logger) Print(v ...any) {
	l.output(0, 2, func(b []byte) []byte {
		return fmt.Append(b, v...)
	})
}

// Printf calls l.Output to print to the logger.
// Arguments are handled in the manner of [fmt.Printf].
func (l *Logger) Printf(format string, v ...any) {
	l.output(0, 2, func(b []byte) []byte {
		return fmt.Appendf(b, format, v...)
	})
}

// Println calls l.Output to print to the logger.
// Arguments are handled in the manner of [fmt.Println].
func (l *Logger) Println(v ...any) {
	l.output(0, 2, func(b []byte) []byte {
		return fmt.Appendln(b, v...)
	})
}

// Fatal is equivalent to l.Print() followed by a call to [os.Exit](1).
func (l *Logger) Fatal(v ...any) {
	l.Output(2, fmt.Sprint(v...))
	os.Exit(1)
}

// Fatalf is equivalent to l.Printf() followed by a call to [os.Exit](1).
func (l *Logger) Fatalf(format string, v ...any) {
	l.Output(2, fmt.Sprintf(format, v...))
	os.Exit(1)
}

// Fatalln is equivalent to l.Println() followed by a call to [os.Exit](1).
func (l *Logger) Fatalln(v ...any) {
	l.Output(2, fmt.Sprintln(v...))
	os.Exit(1)
}

// Panic is equivalent to l.Print() followed by a call to panic().
func (l *Logger) Panic(v ...any) {
	s := fmt.Sprint(v...)
	l.Output(2, s)
	panic(s)
}

// Panicf is equivalent to l.Printf() followed by a call to panic().
func (l *Logger) Panicf(format string, v ...any) {
	s := fmt.Sprintf(format, v...)
	l.Output(2, s)
	panic(s)
}

// Panicln is equivalent to l.Println() followed by a call to panic().
func (l *Logger) Panicln(v ...any) {
	s := fmt.Sprintln(v...)
	l.Output(2, s)
	panic(s)
}

// Flags returns the output flags for the logger.
// The flag bits are [Ldate], [Ltime], and so on.
func (l *Logger) Flags() int {
	return int(l.flag.Load())
}

// SetFlags sets the output flags for the logger.
// The flag bits are [Ldate], [Ltime], and so on.
func (l *Logger) SetFlags(flag int) {
	l.flag.Store(int32(flag))
}

// Prefix returns the output prefix for the logger.
func (l *Logger) Prefix() string {
	if p := l.prefix.Load(); p != nil {
		return *p
	}
	return ""
}

// SetPrefix sets the output prefix for the logger.
func (l *Logger) SetPrefix(prefix string) {
	l.prefix.Store(&prefix)
}

// Writer returns the output destination for the logger.
func (l *Logger) Writer() io.Writer {
	l.outMu.Lock()
	defer l.outMu.Unlock()
	return l.out
}

// SetOutput sets the output destination for the standard logger.
func SetOutput(w io.Writer) {
	std.SetOutput(w)
}

// Flags returns the output flags for the standard logger.
// The flag bits are [Ldate], [Ltime], and so on.
func Flags() int {
	return std.Flags()
}

// SetFlags sets the output flags for the standard logger.
// The flag bits are [Ldate], [Ltime], and so on.
func SetFlags(flag int) {
	std.SetFlags(flag)
}

// Prefix returns the output prefix for the standard logger.
func Prefix() string {
	return std.Prefix()
}

// SetPrefix sets the output prefix for the standard logger.
func SetPrefix(prefix string) {
	std.SetPrefix(prefix)
}

// Writer returns the output destination for the standard logger.
func Writer() io.Writer {
	return std.Writer()
}

// These functions write to the standard logger.

// Print calls Output to print to the standard logger.
// Arguments are handled in the manner of [fmt.Print].
func Print(v ...any) {
	std.output(0, 2, func(b []byte) []byte {
		return fmt.Append(b, v...)
	})
}

// Printf calls Output to print to the standard logger.
// Arguments are handled in the manner of [fmt.Printf].
func Printf(format string, v ...any) {
	std.output(0, 2, func(b []byte) []byte {
		return fmt.Appendf(b, format, v...)
	})
}

// Println calls Output to print to the standard logger.
// Arguments are handled in the manner of [fmt.Println].
func Println(v ...any) {
	std.output(0, 2, func(b []byte) []byte {
		return fmt.Appendln(b, v...)
	})
}

// Fatal is equivalent to [Print] followed by a call to [os.Exit](1).
func Fatal(v ...any) {
	std.Output(2, fmt.Sprint(v...))
	os.Exit(1)
}

// Fatalf is equivalent to [Printf] followed by a call to [os.Exit](1).
func Fatalf(format string, v ...any) {
	std.Output(2, fmt.Sprintf(format, v...))
	os.Exit(1)
}

// Fatalln is equivalent to [Println] followed by a call to [os.Exit](1).
func Fatalln(v ...any) {
	std.Output(2, fmt.Sprintln(v...))
	os.Exit(1)
}

// Panic is equivalent to [Print] followed by a call to panic().
func Panic(v ...any) {
	s := fmt.Sprint(v...)
	std.Output(2, s)
	panic(s)
}

// Panicf is equivalent to [Printf] followed by a call to panic().
func Panicf(format string, v ...any) {
	s := fmt.Sprintf(format, v...)
	std.Output(2, s)
	panic(s)
}

// Panicln is equivalent to [Println] followed by a call to panic().
func Panicln(v ...any) {
	s := fmt.Sprintln(v...)
	std.Output(2, s)
	panic(s)
}

// Output writes the output for a logging event. The string s contains
// the text to print after the prefix specified by the flags of the
// Logger. A newline is appended if the last character of s is not
// already a newline. Calldepth is the count of the number of
// frames to skip when computing the file name and line number
// if [Llongfile] or [Lshortfile] is set; a value of 1 will print the details
// for the caller of Output.
func Output(calldepth int, s string) error {
	return std.Output(calldepth+1, s) // +1 for this frame.
}
