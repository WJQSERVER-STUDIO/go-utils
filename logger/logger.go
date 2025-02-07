/*
Copyright 2024 WJQserver Studio. Open source WSL 1.2 License.
*/

package logger

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 常量定义
const (
	timeFormat     = "02/Jan/2006:15:04:05 -0700" // 日志时间格式
	defaultBufSize = 1000                         // 日志通道的默认缓冲区大小
)

// 日志等级常量
const (
	LevelDump  = iota // Dump 级别，最低级别日志
	LevelDebug        // Debug 级别
	LevelInfo         // Info 级别
	LevelWarn         // Warn 级别
	LevelError        // Error 级别
	LevelNone         // None 级别，不记录日志
)

// 全局变量
var (
	Logw         = Logf                                                        // 快捷方式
	logw         = Logf                                                        // 快捷方式
	logf         = Logf                                                        // 快捷方式
	logger       *log.Logger                                                   // 标准库日志记录器
	logFile      *os.File                                                      // 当前日志文件句柄
	logChannel   = make(chan *logMessage, defaultBufSize)                      // 日志消息通道
	quitChannel  = make(chan struct{})                                         // 用于通知日志系统关闭的通道
	logFileMutex sync.Mutex                                                    // 日志文件操作的互斥锁
	wg           sync.WaitGroup                                                // 用于等待日志协程退出
	logLevel     atomic.Value                                                  // 当前日志等级（使用 atomic.Value 替代 int32）
	initOnce     sync.Once                                                     // 确保 Init 方法只执行一次
	droppedLogs  int64                                                         // 被丢弃的日志数量
	messagePool  = sync.Pool{New: func() interface{} { return &logMessage{} }} // 日志消息池
)

// 日志消息结构体
type logMessage struct {
	level int    // 日志等级
	msg   string // 日志内容
}

var logLevelMap = map[string]int{
	"dump":  LevelDump,
	"debug": LevelDebug,
	"info":  LevelInfo,
	"warn":  LevelWarn,
	"error": LevelError,
	"none":  LevelNone,
}

// SetLogLevel 设置日志等级
func SetLogLevel(level string) error {
	level = strings.ToLower(level)

	if lvl, ok := logLevelMap[level]; ok {
		logLevel.Store(lvl)
		return nil
	}

	return fmt.Errorf("invalid log level: %s", level)
}

// Init 初始化日志记录器
func Init(logFilePath string, maxLogSizeMB int) error {
	var initErr error
	initOnce.Do(func() {
		if err := validateLogFilePath(logFilePath); err != nil {
			initErr = fmt.Errorf("invalid log file path: %w", err)
			return
		}

		logFileMutex.Lock()
		defer logFileMutex.Unlock()

		var err error
		logFile, err = os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			initErr = fmt.Errorf("failed to open log file: %w", err)
			return
		}

		logger = log.New(logFile, "", 0)
		logLevel.Store(LevelDump) // 默认日志等级

		go logWorker()
		go monitorLogSize(logFilePath, int64(maxLogSizeMB)*1024*1024)
	})
	return initErr
}

// validateLogFilePath 验证日志文件路径是否有效
func validateLogFilePath(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist: %s", dir)
	}
	return nil
}

// logWorker 日志处理协程
func logWorker() {
	wg.Add(1)
	defer wg.Done()

	for {
		select {
		case logMsg := <-logChannel:
			logFileMutex.Lock()
			logger.Printf("%s - %s\n", time.Now().Format(timeFormat), logMsg.msg)
			logFileMutex.Unlock()
			messagePool.Put(logMsg) // 回收日志消息对象
		case <-quitChannel:
			for {
				select {
				case logMsg := <-logChannel:
					logFileMutex.Lock()
					logger.Printf("%s - %s\n", time.Now().Format(timeFormat), logMsg.msg)
					logFileMutex.Unlock()
					messagePool.Put(logMsg)
				default:
					return
				}
			}
		}
	}
}

// Log 记录日志
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
		atomic.AddInt64(&droppedLogs, 1)
		fmt.Fprintf(os.Stderr, "Log queue full, dropping message: %s\n", msg)
		messagePool.Put(logMsg) // 回收未使用的日志消息
	}
}

// Logf 格式化日志
func Logf(level int, format string, args ...interface{}) {
	Log(level, fmt.Sprintf(format, args...))
}

// 快捷日志方法
func LogDump(format string, args ...interface{})    { Logf(LevelDump, "[DUMP] "+format, args...) }
func LogDebug(format string, args ...interface{})   { Logf(LevelDebug, "[DEBUG] "+format, args...) }
func LogInfo(format string, args ...interface{})    { Logf(LevelInfo, "[INFO] "+format, args...) }
func LogWarning(format string, args ...interface{}) { Logf(LevelWarn, "[WARNING] "+format, args...) }
func LogError(format string, args ...interface{})   { Logf(LevelError, "[ERROR] "+format, args...) }

// Close 关闭日志系统
func Close() {
	close(quitChannel)
	wg.Wait()

	logFileMutex.Lock()
	defer logFileMutex.Unlock()
	if logFile != nil {
		if err := logFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing log file: %v\n", err)
		}
	}
}

// monitorLogSize 定期检查日志文件大小
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

// rotateLogFile 轮转日志文件
func rotateLogFile(logFilePath string) error {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()

	if logFile != nil {
		if err := logFile.Close(); err != nil {
			return fmt.Errorf("error closing log file: %w", err)
		}
	}

	backupPath := fmt.Sprintf("%s.%s", logFilePath, time.Now().Format("20060102-150405"))
	if err := os.Rename(logFilePath, backupPath); err != nil {
		return fmt.Errorf("error renaming log file: %w", err)
	}

	newFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("error creating new log file: %w", err)
	}
	logFile = newFile
	logger.SetOutput(logFile)

	go func() {
		if err := compressLog(backupPath); err != nil {
			LogError("Compression failed: %v", err)
		}
	}()

	return nil
}

// compressLog 压缩日志文件
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
