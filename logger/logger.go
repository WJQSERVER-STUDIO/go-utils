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
	"sync"
	"time"
)

const (
	timeFormat     = "02/Jan/2006:15:04:05 -0700"
	defaultBufSize = 1000
)

// 日志等级常量
const (
	LevelDump = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelNone
)

var (
	Logw         = Logf
	logw         = Logf
	logf         = Logf
	logFile      *os.File
	logger       *log.Logger
	logChannel   = make(chan logMessage, defaultBufSize)
	quitChannel  = make(chan struct{})
	logFileMutex sync.Mutex
	wg           sync.WaitGroup
	logLevel     = LevelDump // 默认日志等级为 Dump
)

// 日志消息结构体
type logMessage struct {
	level int
	msg   string
}

// SetLogLevel 设置日志等级
func SetLogLevel(level string) {
	switch level {
	case "dump":
		logLevel = LevelDump
	case "debug":
		logLevel = LevelDebug
	case "info":
		logLevel = LevelInfo
	case "warn":
		logLevel = LevelWarn
	case "error":
		logLevel = LevelError
	case "none":
		logLevel = LevelNone
	default:
		logLevel = LevelDump // 默认等级为 Dump
	}
}

// Init 初始化日志记录器
func Init(logFilePath string, maxLogSizeMB int) error {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()

	var err error
	logFile, err = os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	logger = log.New(logFile, "", 0)
	go logWorker()
	go monitorLogSize(logFilePath, int64(maxLogSizeMB)*1024*1024)
	return nil
}

func logWorker() {
	wg.Add(1)
	defer wg.Done()

	for {
		select {
		case logMsg := <-logChannel:
			// 过滤日志等级
			if logMsg.level >= logLevel {
				logFileMutex.Lock()
				logger.Printf("%s - %s\n", time.Now().Format(timeFormat), logMsg.msg)
				logFileMutex.Unlock()
			}
		case <-quitChannel:
			// 处理剩余日志
			for {
				select {
				case logMsg := <-logChannel:
					if logMsg.level >= logLevel {
						logFileMutex.Lock()
						logger.Printf("%s - %s\n", time.Now().Format(timeFormat), logMsg.msg)
						logFileMutex.Unlock()
					}
				default:
					return
				}
			}
		}
	}
}

// Log 记录日志
func Log(level int, msg string) {
	select {
	case logChannel <- logMessage{level: level, msg: msg}:
	default:
		// 日志队列满时丢弃日志并通知
		fmt.Fprintf(os.Stderr, "Log queue full, dropping message: %s\n", msg)
	}
}

// Logf 格式化日志
func Logf(level int, format string, args ...interface{}) {
	Log(level, fmt.Sprintf(format, args...))
}

// LogDump Dump级别日志
func LogDump(format string, args ...interface{}) {
	Logf(LevelDump, "[DUMP] "+format, args...)
}

// LogDebug Debug级别日志
func LogDebug(format string, args ...interface{}) {
	Logf(LevelDebug, "[DEBUG] "+format, args...)
}

// LogInfo 信息级别日志
func LogInfo(format string, args ...interface{}) {
	Logf(LevelInfo, "[INFO] "+format, args...)
}

// LogWarning 警告级别日志
func LogWarning(format string, args ...interface{}) {
	Logf(LevelWarn, "[WARNING] "+format, args...)
}

// LogError 错误级别日志
func LogError(format string, args ...interface{}) {
	Logf(LevelError, "[ERROR] "+format, args...)
}

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

func rotateLogFile(logFilePath string) error {
	logFileMutex.Lock()
	defer logFileMutex.Unlock()

	// 关闭当前日志文件
	if logFile != nil {
		if err := logFile.Close(); err != nil {
			return fmt.Errorf("error closing log file: %w", err)
		}
	}

	// 重命名原日志文件
	backupPath := fmt.Sprintf("%s.%s", logFilePath, time.Now().Format("20060102-150405"))
	if err := os.Rename(logFilePath, backupPath); err != nil {
		return fmt.Errorf("error renaming log file: %w", err)
	}

	// 创建新日志文件
	newFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("error creating new log file: %w", err)
	}
	logFile = newFile
	logger.SetOutput(logFile)

	// 异步压缩旧日志
	go func() {
		if err := compressLog(backupPath); err != nil {
			LogError("Compression failed: %v", err)
		}
		os.Remove(backupPath)
	}()

	return nil
}

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
