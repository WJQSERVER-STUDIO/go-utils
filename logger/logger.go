/*
Copyright 2024 WJQserver Studio. WJQserver Studio 2.0 License.
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
	//"github.com/WJQSERVER-STUDIO/go-utils/log"
)

// 常量定义
const (
	timeFormat = time.RFC3339 // 日志时间格式
)

// 日志等级常量
const (
	LevelDump  = iota // 记录所有日志
	LevelDebug        // 调试日志
	LevelInfo         // 信息日志
	LevelWarn         // 警告日志
	LevelError        // 错误日志
	LevelNone         // 不记录日志
)

// 日志等级映射表
var logLevelMap = map[string]int{
	"dump":  LevelDump,
	"debug": LevelDebug,
	"info":  LevelInfo,
	"warn":  LevelWarn,
	"error": LevelError,
	"none":  LevelNone,
}

// Logger 结构体封装了日志记录器的功能
type Logger struct {
	logger       *log.Logger  // 日志记录器实例
	logFile      *os.File     // 日志文件句柄
	logLevel     atomic.Value // 当前日志等级
	logFileMutex sync.Mutex   // 互斥锁，确保线程安全
	maxLogSizeMB int64        // 最大日志文件大小（MB）
	initOnce     sync.Once    // 确保初始化只执行一次
	droppedLogs  int64        // 统计丢弃的日志数量（未使用）
}

// NewLogger 创建一个新的 Logger 实例
func NewLogger() *Logger {
	l := &Logger{
		logLevel:     atomic.Value{}, // 初始化 atomic.Value
		maxLogSizeMB: 100,            // 默认最大日志大小 100MB
	}
	l.logLevel.Store(LevelDump) // 默认日志级别为 LevelDump
	return l
}

// SetLogLevel 设置日志等级
func (l *Logger) SetLogLevelStruct(level string) error {
	level = strings.ToLower(level) // 转换为小写以进行匹配
	if lvl, ok := logLevelMap[level]; ok {
		l.logLevel.Store(lvl) // 存储新的日志等级
		return nil
	}
	return fmt.Errorf("invalid log level: %s", level) // 返回错误信息
}

// Init 初始化日志记录器
func (l *Logger) InitStruct(logFilePath string) error {
	var initErr error
	l.initOnce.Do(func() {
		if err := l.validateLogFilePath(logFilePath); err != nil {
			initErr = fmt.Errorf("invalid log file path: %w", err)
			return
		}

		l.logFileMutex.Lock()
		defer l.logFileMutex.Unlock()

		var err error
		l.logFile, err = os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			initErr = fmt.Errorf("failed to open log file: %w", err)
			return
		}

		// 移除标准日志标志，以便手动控制时间格式
		l.logger = log.New(l.logFile, "", 0)
		go l.monitorLogSize(logFilePath, l.maxLogSizeMB*1024*1024) // 启动日志文件大小监控
	})
	return initErr
}

// validateLogFilePath 验证日志文件路径的有效性
func (l *Logger) validateLogFilePath(path string) error {
	dir := filepath.Dir(path) // 获取目录路径
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("directory does not exist: %s", dir) // 返回目录不存在的错误
	}
	return nil
}

// SetMaxLogSizeMB 设置最大日志文件大小（MB）
func (l *Logger) SetMaxLogSizeMBStruct(maxSizeMB int) {
	l.maxLogSizeMB = int64(maxSizeMB) // 更新最大日志大小
}

// Log 记录日志
func (l *Logger) LogStruct(level int, msg string) {
	if level < l.logLevel.Load().(int) {
		return // 如果当前日志等级低于设定等级，则不记录
	}

	logPrefix := ""
	switch level {
	case LevelDump:
		logPrefix = "[DUMP] "
	case LevelDebug:
		logPrefix = "[DEBUG] "
	case LevelInfo:
		logPrefix = "[INFO] "
	case LevelWarn:
		logPrefix = "[WARNING] "
	case LevelError:
		logPrefix = "[ERROR] "
	}

	l.logFileMutex.Lock()
	defer l.logFileMutex.Unlock()
	// 手动格式化时间并记录日志
	l.logger.Printf("%s - %s%s", time.Now().Format(timeFormat), logPrefix, msg)
}

// Logf 格式化日志记录
func (l *Logger) LogfStruct(level int, format string, args ...interface{}) {
	l.LogStruct(level, fmt.Sprintf(format, args...)) // 调用 LogStruct 记录格式化日志
}

// LogDump 快捷日志方法
func (l *Logger) LogDumpStruct(format string, args ...interface{}) {
	l.LogfStruct(LevelDump, format, args...) // 记录 DUMP 级别日志
}

// LogDebug 快捷日志方法
func (l *Logger) LogDebugStruct(format string, args ...interface{}) {
	l.LogfStruct(LevelDebug, format, args...) // 记录 DEBUG 级别日志
}

// LogInfo 快捷日志方法
func (l *Logger) LogInfoStruct(format string, args ...interface{}) {
	l.LogfStruct(LevelInfo, format, args...) // 记录 INFO 级别日志
}

// LogWarning 快捷日志方法
func (l *Logger) LogWarningStruct(format string, args ...interface{}) {
	l.LogfStruct(LevelWarn, format, args...) // 记录 WARNING 级别日志
}

// LogError 快捷日志方法
func (l *Logger) LogErrorStruct(format string, args ...interface{}) {
	l.LogfStruct(LevelError, format, args...) // 记录 ERROR 级别日志
}

// Close 关闭日志系统
func (l *Logger) CloseStruct() {
	l.logFileMutex.Lock()
	defer l.logFileMutex.Unlock()
	if l.logFile != nil {
		if err := l.logFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing log file: %v\n", err) // 输出关闭日志文件时的错误
		}
		l.logFile = nil // 确保在关闭后将 logFile 设置为 nil
	}
}

// monitorLogSize 定期检查日志文件大小
func (l *Logger) monitorLogSize(logFilePath string, maxBytes int64) {
	ticker := time.NewTicker(15 * time.Minute) // 每 15 分钟检查一次
	defer ticker.Stop()

	for range ticker.C {
		l.logFileMutex.Lock()
		info, err := l.logFile.Stat() // 获取日志文件信息
		l.logFileMutex.Unlock()

		if err == nil && info.Size() > maxBytes {
			if err := l.rotateLogFile(logFilePath); err != nil {
				l.LogErrorStruct("Log rotation failed: %v", err) // 记录日志轮转失败的错误
			}
		}
	}
}

// rotateLogFile 轮转日志文件
func (l *Logger) rotateLogFile(logFilePath string) error {
	l.logFileMutex.Lock()
	defer l.logFileMutex.Unlock()

	if l.logFile != nil {
		if err := l.logFile.Close(); err != nil {
			return fmt.Errorf("error closing log file: %w", err) // 返回关闭日志文件时的错误
		}
	}

	backupPath := fmt.Sprintf("%s.%s", logFilePath, time.Now().Format("20060102-150405")) // 生成备份文件名
	if err := os.Rename(logFilePath, backupPath); err != nil {
		return fmt.Errorf("error renaming log file: %w", err) // 返回重命名日志文件时的错误
	}

	newFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("error creating new log file: %w", err) // 返回创建新日志文件时的错误
	}
	l.logFile = newFile
	l.logger.SetOutput(l.logFile) // 更新 logger 的输出目标

	go func() {
		if err := l.compressLog(backupPath); err != nil {
			l.LogErrorStruct("Compression failed: %v", err) // 记录压缩失败的错误
		}
		if err := os.Remove(backupPath); err != nil {
			l.LogErrorStruct("Failed to remove backup file: %v", err) // 记录删除备份文件失败的错误
			fmt.Printf("Failed to remove backup file: %v\n", err)
		}
	}()

	return nil
}

// compressLog 压缩日志文件
func (l *Logger) compressLog(srcPath string) error {
	srcFile, err := os.Open(srcPath) // 打开源日志文件
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(srcPath + ".tar.gz") // 创建压缩文件
	if err != nil {
		return err
	}
	defer dstFile.Close()

	gzWriter := gzip.NewWriter(dstFile) // 创建 gzip 写入器
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter) // 创建 tar 写入器
	defer tarWriter.Close()

	info, err := srcFile.Stat() // 获取源文件信息
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name:    filepath.Base(srcPath), // 设置 tar 头部信息
		Size:    info.Size(),
		Mode:    int64(info.Mode()),
		ModTime: info.ModTime(),
	}

	if err := tarWriter.WriteHeader(header); err != nil {
		return err // 写入 tar 头部时的错误
	}

	if _, err := io.Copy(tarWriter, srcFile); err != nil {
		return err // 复制文件内容时的错误
	}

	return nil
}

// 全局 Logger 实例
var defaultLogger = NewLogger()

// 导出的全局函数，兼容原有函数名
var (
	Logw = Logf // 兼容原有快捷方式变量
	logw = Logf
	logf = Logf
)

// 导出全局函数，使用原有的函数名称，调用 defaultLogger 的方法
// 初始化和配置
func Init(logFilePath string, maxLogSizeMB int) error {
	defaultLogger.SetMaxLogSizeMBStruct(maxLogSizeMB) // 设置最大日志大小
	return defaultLogger.InitStruct(logFilePath)      // 调用内部的 InitStruct
}

// 设置日志等级
func SetLogLevel(level string) error {
	return defaultLogger.SetLogLevelStruct(level) // 调用内部的 SetLogLevelStruct
}

// 设置最大日志文件大小（MB）
func SetMaxLogSizeMB(maxSizeMB int) {
	defaultLogger.SetMaxLogSizeMBStruct(maxSizeMB) // 调用内部的 SetMaxLogSizeMBStruct
}

// 关闭日志系统
func Close() {
	defaultLogger.CloseStruct() // 调用内部的 CloseStruct
}

// 日志记录函数，使用原有的函数名称
func Log(level int, msg string) {
	defaultLogger.LogStruct(level, msg) // 调用内部的 LogStruct
}

// 格式化日志记录函数，使用原有的函数名称
func Logf(level int, format string, args ...interface{}) {
	defaultLogger.LogfStruct(level, format, args...) // 调用内部的 LogfStruct
}

// 不同日志等级的快捷函数，使用原有的函数名称
func LogDump(format string, args ...interface{}) {
	defaultLogger.LogDumpStruct(format, args...) // 调用内部的 LogDumpStruct
}

func LogDebug(format string, args ...interface{}) {
	defaultLogger.LogDebugStruct(format, args...) // 调用内部的 LogDebugStruct
}

func LogInfo(format string, args ...interface{}) {
	defaultLogger.LogInfoStruct(format, args...) // 调用内部的 LogInfoStruct
}

func LogWarning(format string, args ...interface{}) {
	defaultLogger.LogWarningStruct(format, args...) // 调用内部的 LogWarningStruct
}

func LogError(format string, args ...interface{}) {
	defaultLogger.LogErrorStruct(format, args...) // 调用内部的 LogErrorStruct
}
