/*
Copyright 2024 WJQserver Studio. Open source WSL 1.2 License.
*/

package logger

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 常量定义
const (
	timeFormat     = time.RFC3339 // 日志时间格式, 更加通用和精确
	defaultBufSize = 4096         // 日志通道的默认缓冲区大小，调整为 4KB，可以根据实际情况调整
)

// 日志等级常量 (保持不变)
const (
	LevelDump = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelNone
)

// 全局变量
var (
	Logw          = Logf // 快捷方式 (保持不变)
	logw          = Logf // 快捷方式 (保持不变)
	logf          = Logf // 快捷方式 (保持不变)
	logFile       *os.File
	logWriter     *bufio.Writer                                         // 使用 bufio.Writer 进行缓冲写
	logChannel    chan *logMessage                                      // 日志消息通道 (保持不变)
	quitChannel   chan struct{}                                         // 关闭通道 (保持不变)
	logFileMutex  sync.Mutex                                            // 文件锁 (保持不变)
	wg            sync.WaitGroup                                        // WaitGroup (保持不变)
	logLevel      atomic.Value                                          // 原子日志等级 (保持不变)
	initOnce      sync.Once                                             // Init 单例 (保持不变)
	droppedLogs   atomic.Int64                                          // 原子丢弃计数
	messagePool   = sync.Pool{New: func() any { return &logMessage{} }} // 消息池 (保持不变)
	flushTicker   *time.Ticker                                          // 定时刷盘的 ticker
	flushInterval = 1 * time.Second                                     // 日志刷盘间隔，例如 1 秒
)

// 日志消息结构体 (保持不变)
type logMessage struct {
	level int
	msg   string
}

// 日志等级映射表 (保持不变)
var logLevelMap = map[string]int{
	"dump":  LevelDump,
	"debug": LevelDebug,
	"info":  LevelInfo,
	"warn":  LevelWarn,
	"error": LevelError,
	"none":  LevelNone,
}

// SetLogLevel 设置日志等级 (保持不变)
func SetLogLevel(level string) error {
	level = strings.ToLower(level)
	if lvl, ok := logLevelMap[level]; ok {
		logLevel.Store(lvl)
		return nil
	}
	return fmt.Errorf("invalid log level: %s", level)
}

// Init 初始化日志记录器
func Init(logFilePath string, maxLogSizeMB int, flushLogInterval time.Duration) error {
	var initErr error
	initOnce.Do(func() {

		flushInterval = flushLogInterval

		if err := validateLogFilePath(logFilePath); err != nil {
			initErr = fmt.Errorf("invalid log file path: %w", err)
			return
		}

		logFileMutex.Lock()
		defer logFileMutex.Unlock()

		file, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			initErr = fmt.Errorf("failed to open log file: %w", err)
			return
		}
		logFile = file
		logWriter = bufio.NewWriterSize(logFile, defaultBufSize) // 初始化带缓冲的 Writer
		logLevel.Store(LevelDump)

		logChannel = make(chan *logMessage, defaultBufSize) // 初始化 channel
		quitChannel = make(chan struct{})                   // 初始化 quit channel
		flushTicker = time.NewTicker(flushInterval)         // 初始化定时器

		go logWorker()                                                // 启动日志处理协程
		go monitorLogSize(logFilePath, int64(maxLogSizeMB)*1024*1024) // 启动日志文件大小监控
		go flushWorker()                                              // 启动刷盘协程
	})
	return initErr
}

// validateLogFilePath 验证日志文件路径 (保持不变)
func validateLogFilePath(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist: %s", dir)
	}
	return nil
}

// flushWorker 定时刷盘协程
func flushWorker() {
	wg.Add(1)
	defer wg.Done()

	for {
		select {
		case <-flushTicker.C:
			if err := flush(); err != nil {
				fmt.Fprintf(os.Stderr, "Error flushing log: %v\n", err)
			}
		case <-quitChannel:
			flushTicker.Stop()              // 关闭定时器
			if err := flush(); err != nil { // 最后一次刷盘
				fmt.Fprintf(os.Stderr, "Error flushing log during shutdown: %v\n", err)
			}
			return
		}
	}
}

// flush 刷盘操作
func flush() error {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()
	if logWriter != nil {
		return logWriter.Flush() // 使用 bufio.Writer 的 Flush 方法
	}
	return nil
}

// logWorker 日志处理协程 (改进版)
func logWorker() {
	wg.Add(1)
	defer wg.Done()

	for {
		select {
		case logMsg := <-logChannel:
			logFileMutex.Lock()                                                                                  // 锁的粒度更小，只在写入时加锁
			_, err := logWriter.WriteString(fmt.Sprintf("%s - %s\n", time.Now().Format(timeFormat), logMsg.msg)) // 直接写入 bufio.Writer
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to write log message: %v\n", err) // 写入错误处理
			}
			logFileMutex.Unlock()
			messagePool.Put(logMsg) // 回收消息
		case <-quitChannel:
			for {
				select {
				case logMsg := <-logChannel: // 处理剩余消息
					logFileMutex.Lock()
					_, err := logWriter.WriteString(fmt.Sprintf("%s - %s\n", time.Now().Format(timeFormat), logMsg.msg))
					if err != nil {
						fmt.Fprintf(os.Stderr, "Failed to write log message during shutdown: %v\n", err)
					}
					logFileMutex.Unlock()
					messagePool.Put(logMsg)
				default:
					return // 通道已空，退出
				}
			}
		}
	}
}

// Log 记录日志 (保持不变)
func Log(level int, msg string) {
	if level < logLevel.Load().(int) {
		return
	}

	logMsg := messagePool.Get().(*logMessage)
	logMsg.level = level
	logMsg.msg = msg

	select {
	case logChannel <- logMsg:
	default:
		droppedLogs.Add(1) // 使用原子操作
		fmt.Fprintf(os.Stderr, "Log queue full, dropping message: %s\n", msg)
		messagePool.Put(logMsg)
	}
}

// Logf 格式化日志 (保持不变)
func Logf(level int, format string, args ...interface{}) {
	Log(level, fmt.Sprintf(format, args...))
}

// 快捷日志方法 (保持不变)
func LogDump(format string, args ...interface{})    { Logf(LevelDump, "[DUMP] "+format, args...) }
func LogDebug(format string, args ...interface{})   { Logf(LevelDebug, "[DEBUG] "+format, args...) }
func LogInfo(format string, args ...interface{})    { Logf(LevelInfo, "[INFO] "+format, args...) }
func LogWarning(format string, args ...interface{}) { Logf(LevelWarn, "[WARNING] "+format, args...) }
func LogError(format string, args ...interface{})   { Logf(LevelError, "[ERROR] "+format, args...) }

// Close 关闭日志系统 (改进版)
func Close() {
	close(quitChannel)
	wg.Wait() // 等待日志 worker 和 flush worker

	logFileMutex.Lock()
	defer logFileMutex.Unlock()

	if logWriter != nil {
		if err := logWriter.Flush(); err != nil { // 确保所有缓冲数据刷入磁盘
			fmt.Fprintf(os.Stderr, "Error flushing log before close: %v\n", err)
		}
		logWriter = nil // 释放 Writer
	}
	if logFile != nil {
		if err := logFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing log file: %v\n", err)
		}
		logFile = nil // 释放 File
	}
	flushTicker = nil // 释放 ticker
}

// monitorLogSize 定期检查日志文件大小 (保持不变)
func monitorLogSize(logFilePath string, maxBytes int64) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logFileMutex.Lock()
			info, err := logFile.Stat()
			logFileMutex.Unlock()

			if err == nil && info.Size() > maxBytes {
				if err := rotateLogFile(logFilePath); err != nil {
					LogError("Log rotation failed: %v", err)
				}
			}
		case <-quitChannel:
			return
		}
	}
}

// rotateLogFile 轮转日志文件 (保持不变)
func rotateLogFile(logFilePath string) error {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()

	if logFile != nil {
		if err := logFile.Close(); err != nil {
			return fmt.Errorf("error closing log file: %w", err)
		}
		logFile = nil   // 释放旧的 file
		logWriter = nil // 释放旧的 writer
	}

	backupPath := fmt.Sprintf("%s.%s", logFilePath, time.Now().Format("20060102-150405"))
	if err := os.Rename(logFilePath, backupPath); err != nil {
		return fmt.Errorf("error renaming log file: %w", err)
	}

	newFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("error creating new log file: %w", err)
	}
	logFile = newFile                                        // 更新 file
	logWriter = bufio.NewWriterSize(logFile, defaultBufSize) // 初始化新的 writer
	//logger = nil                                             // 卸载 logger (虽然代码里没有用到 logger 了，但为了代码完整性)

	go func() {
		if err := compressLog(backupPath); err != nil {
			LogError("Compression failed: %v", err)
		}
		if err := os.Remove(backupPath); err != nil {
			LogError("Failed to remove backup file: %v", err)
			fmt.Printf("Failed to remove backup file: %v\n", err) // 增加 fmt.Printf 输出，方便调试
		}
	}()

	return nil
}

// compressLog 压缩日志文件 (保持不变)
func compressLog(srcPath string) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(srcPath + ".tar.gz")
	if err != nil {
		return err
	}
	defer dstFile.Close()

	gzWriter := gzip.NewWriter(dstFile)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name:    filepath.Base(srcPath),
		Size:    info.Size(),
		Mode:    int64(info.Mode()),
		ModTime: info.ModTime(),
	}

	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}

	if _, err := io.Copy(tarWriter, srcFile); err != nil {
		return err
	}

	return nil
}

// DumpDroppedLogs 返回丢弃的日志数量
func DumpDroppedLogs() int64 {
	return droppedLogs.Load()
}
