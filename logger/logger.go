/*
Copyright 2024 WJQserver Studio. Open source WSL 1.2 License.
*/

package logger

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog" // 导入 slog
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 常量定义
const (
	timeFormat     = time.RFC3339 // 日志时间格式 (slog 默认处理时间，这里可以保留作为参考)
	defaultBufSize = 1000         // 日志通道的默认缓冲区大小
)

// 日志等级常量 (slog 已经有内置的等级，这里可以考虑对齐或者保持现有定义)
const (
	LevelDump  = iota - 4 // Dump 级别，最低级别日志，对应 slog.LevelDebug -4
	LevelDebug            // Debug 级别，对应 slog.LevelDebug
	LevelInfo             // Info 级别，对应 slog.LevelInfo
	LevelWarn             // Warn 级别，对应 slog.LevelWarn
	LevelError            // Error 级别，对应 slog.LevelError
	LevelNone             // None 级别，不记录日志，高于 Error
)

// 全局变量
var (
	Logw         = Logf                                                        // 快捷方式
	logw         = Logf                                                        // 快捷方式
	logf         = Logf                                                        // 快捷方式
	logger       *slog.Logger                                                  // 使用 slog.Logger
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

// 日志等级映射表 (需要调整以匹配 slog 的 Level 类型)
var logLevelMap = map[string]int{
	"dump":  LevelDump,  // 对应 slog.LevelDebug -4
	"debug": LevelDebug, // 对应 slog.LevelDebug
	"info":  LevelInfo,  // 对应 slog.LevelInfo
	"warn":  LevelWarn,  // 对应 slog.LevelWarn
	"error": LevelError, // 对应 slog.LevelError
	"none":  LevelNone,  // 高于 slog.LevelError，用于关闭日志
}

// SetLogLevel 设置日志等级
// 参数：level 字符串形式的日志等级（如 "debug"）
// 返回：如果输入的日志等级有效，返回 nil；否则返回错误信息。
func SetLogLevel(level string) error {
	level = strings.ToLower(level)

	if lvl, ok := logLevelMap[level]; ok {
		logLevel.Store(lvl) // 使用原子操作存储日志等级
		return nil
	}

	return fmt.Errorf("invalid log level: %s", level) // 返回无效日志等级的错误
}

// Init 初始化日志记录器
// 参数：logFilePath 日志文件路径；maxLogSizeMB 日志文件最大大小（MB）
// 返回：初始化过程中遇到的错误，如果没有错误则返回 nil。
func Init(logFilePath string, maxLogSizeMB int) error {
	var initErr error
	initOnce.Do(func() { // 确保只执行一次
		if err := validateLogFilePath(logFilePath); err != nil {
			initErr = fmt.Errorf("invalid log file path: %w", err)
			return
		}

		logFileMutex.Lock() // 加锁以确保安全访问
		defer logFileMutex.Unlock()

		var err error
		logFile, err = os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666) // 打开日志文件
		if err != nil {
			initErr = fmt.Errorf("failed to open log file: %w", err)
			return
		}

		// 创建 slog.Logger，使用 TextHandler 输出到文件，并自定义格式
		textHandler := slog.NewTextHandler(logFile, &slog.HandlerOptions{
			Level: slog.LevelDebug, // 默认最低等级设置为 Debug，后续会根据 SetLogLevel 调整
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					// 格式化时间，例如 "2025-03-07T14:50:13+08:00" 这种格式 (RFC3339)
					a.Value = slog.StringValue(a.Value.Time().Format(time.RFC3339))
					return a
				}
				if a.Key == slog.LevelKey {
					levelString := ""
					level := a.Value.Any().(slog.Level)
					switch level {
					case slog.LevelDebug - 4:
						levelString = "[DUMP]"
					case slog.LevelDebug:
						levelString = "[DEBUG]"
					case slog.LevelInfo:
						levelString = "[INFO]"
					case slog.LevelWarn:
						levelString = "[WARN]"
					case slog.LevelError:
						levelString = "[ERROR]"
					default:
						levelString = "[UNKNOWN]" //  或者您可以选择忽略，如果默认级别就够用
					}
					return slog.String(a.Key, levelString) // 保留 "level" 键，但值替换为带括号的级别字符串
				}
				if a.Key == slog.MessageKey {
					return slog.String(a.Key, a.Value.String()) // 保留 "msg" 键，值保持消息字符串
				}
				return a // 对于其他 attributes，保持原样
			},
			// Format: // 您也可以考虑完全自定义 Format 函数，如果 ReplaceAttr 不够灵活
		})
		logger = slog.New(textHandler) // 创建新的 slog.Logger

		logLevel.Store(LevelDump) // 默认日志等级为 Dump

		go logWorker()                                                // 启动日志处理协程
		go monitorLogSize(logFilePath, int64(maxLogSizeMB)*1024*1024) // 启动日志文件大小监控
	})
	return initErr // 返回初始化错误
}

// validateLogFilePath 验证日志文件路径是否有效
// 参数：path 日志文件路径
// 返回：如果路径有效则返回 nil；否则返回错误信息。
func validateLogFilePath(path string) error {
	dir := filepath.Dir(path)                       // 获取目录部分
	if _, err := os.Stat(dir); os.IsNotExist(err) { // 检查目录是否存在
		return fmt.Errorf("directory does not exist: %s", dir)
	}
	return nil // 返回 nil 表示路径有效
}

// logWorker 日志处理协程
// 该协程负责从日志通道中读取日志消息并将其写入日志文件。
func logWorker() {
	wg.Add(1)       // 增加等待组计数
	defer wg.Done() // 协程结束时减少计数

	for {
		select {
		case logMsg := <-logChannel: // 从日志通道接收消息
			currentLevel := logLevel.Load().(int)
			if logMsg.level < currentLevel {
				messagePool.Put(logMsg) // 低于当前日志等级，回收消息并跳过
				continue
			}

			// 使用 slog 记录日志，传递时间和级别作为 attributes
			var level slog.Level
			switch logMsg.level {
			case LevelDump:
				level = slog.LevelDebug - 4 // 保持 Dump 低于 Debug
			case LevelDebug:
				level = slog.LevelDebug
			case LevelInfo:
				level = slog.LevelInfo
			case LevelWarn:
				level = slog.LevelWarn
			case LevelError:
				level = slog.LevelError
			default:
				level = slog.LevelDebug // 默认使用 Debug 等级
			}

			// 使用 slog.Log 记录日志，添加时间、级别和消息 attributes
			logger.Log(
				context.Background(),
				level,
				logMsg.msg,
				// 可以选择移除时间属性，因为 TextHandler 默认会添加时间，我们已经在 ReplaceAttr 中格式化了
				// slog.Time(time.Now()),
				// slog.String("level", LevelName(level)), //  级别信息会被 TextHandler 自动处理，不需要手动添加，除非你想自定义级别名称
			)
			messagePool.Put(logMsg) // 回收日志消息对象

		case <-quitChannel: // 接收到关闭信号
			for {
				select {
				case logMsg := <-logChannel: // 继续处理未处理的日志消息
					currentLevel := logLevel.Load().(int)
					if logMsg.level < currentLevel {
						messagePool.Put(logMsg) // 低于当前日志等级，回收消息并跳过
						continue
					}
					var level slog.Level
					switch logMsg.level {
					case LevelDump:
						level = slog.LevelDebug - 4 // 保持 Dump 低于 Debug
					case LevelDebug:
						level = slog.LevelDebug
					case LevelInfo:
						level = slog.LevelInfo
					case LevelWarn:
						level = slog.LevelWarn
					case LevelError:
						level = slog.LevelError
					default:
						level = slog.LevelDebug // 默认使用 Debug 等级
					}
					logger.Log(context.Background(), level, logMsg.msg) // 使用 slog.Log 记录日志
					messagePool.Put(logMsg)
				default:
					return // 无更多消息，退出
				}
			}
		}
	}
}

// Log 记录日志
// 参数：level 日志等级；msg 日志内容
// 该函数会检查当前日志等级，如果日志等级低于设定的等级，则不记录日志。
func Log(level int, msg string) {
	// 日志等级检查已经在 logWorker 中完成，这里只需要发送消息到通道
	logMsg := messagePool.Get().(*logMessage) // 从池中获取日志消息对象
	logMsg.level = level
	logMsg.msg = msg

	select {
	case logChannel <- logMsg: // 尝试将日志消息发送到通道
	default: // 如果通道满
		atomic.AddInt64(&droppedLogs, 1)                                      // 增加丢弃日志计数
		fmt.Fprintf(os.Stderr, "Log queue full, dropping message: %s\n", msg) // 输出警告
		messagePool.Put(logMsg)                                               // 回收未使用的日志消息
	}
}

// Logf 格式化日志
// 参数：level 日志等级；format 格式化字符串；args 格式化参数
// 该函数将格式化后的字符串作为日志内容记录。
func Logf(level int, format string, args ...interface{}) {
	Log(level, fmt.Sprintf(format, args...)) // 格式化日志并记录
}

// 快捷日志方法
// 这些方法用于不同日志等级的快捷记录，方便使用。
func LogDump(format string, args ...interface{}) {
	Logf(LevelDump, "[DUMP] "+format, args...)
}
func LogDebug(format string, args ...interface{}) {
	Logf(LevelDebug, "[DEBUG] "+format, args...)
}
func LogInfo(format string, args ...interface{}) {
	Logf(LevelInfo, "[INFO] "+format, args...)
}
func LogWarning(format string, args ...interface{}) {
	Logf(LevelWarn, "[WARNING] "+format, args...)
}
func LogError(format string, args ...interface{}) {
	Logf(LevelError, "[ERROR] "+format, args...)
}

// Close 关闭日志系统
// 该函数会关闭日志通道并等待日志处理协程完成。
func Close() {
	close(quitChannel) // 发送关闭信号
	wg.Wait()          // 等待所有协程完成

	logFileMutex.Lock() // 加锁以确保安全关闭
	defer logFileMutex.Unlock()
	if logFile != nil {
		if err := logFile.Close(); err != nil { // 关闭日志文件
			fmt.Fprintf(os.Stderr, "Error closing log file: %v\n", err)
		}
	}
}

// monitorLogSize 定期检查日志文件大小
// 参数：logFilePath 日志文件路径；maxBytes 最大字节数
// 该函数每 15 分钟检查一次日志文件大小，超过限制时进行轮转。
func monitorLogSize(logFilePath string, maxBytes int64) {
	ticker := time.NewTicker(15 * time.Minute) // 创建定时器
	defer ticker.Stop()                        // 确保停止定时器

	for {
		select {
		case <-ticker.C: // 每次定时器触发
			logFileMutex.Lock()         // 加锁以确保安全访问
			info, err := logFile.Stat() // 获取日志文件信息
			logFileMutex.Unlock()

			if err == nil && info.Size() > maxBytes { // 检查文件大小
				if err := rotateLogFile(logFilePath); err != nil { // 如果超出大小，进行轮转
					LogError("Log rotation failed: %v", err) // 注意这里也要用 slog 的 Error
				}
			}
		case <-quitChannel: // 接收到关闭信号
			return // 退出循环
		}
	}
}

// rotateLogFile 轮转日志文件
// 参数：logFilePath 日志文件路径
// 该函数将当前日志文件重命名并创建一个新的日志文件。
func rotateLogFile(logFilePath string) error {
	logFileMutex.Lock() // 加锁以确保安全操作
	defer logFileMutex.Unlock()

	if logFile != nil {
		if err := logFile.Close(); err != nil { // 关闭当前日志文件
			return fmt.Errorf("error closing log file: %w", err)
		}
	}

	backupPath := fmt.Sprintf("%s.%s", logFilePath, time.Now().Format("20060102-150405")) // 生成备份文件路径
	if err := os.Rename(logFilePath, backupPath); err != nil {                            // 重命名当前日志文件
		return fmt.Errorf("error renaming log file: %w", err)
	}

	newFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666) // 创建新的日志文件
	if err != nil {
		return fmt.Errorf("error creating new log file: %w", err)
	}
	logFile = newFile // 更新当前日志文件句柄
	// logger.SetOutput(logFile) // slog 不需要 SetOutput，handler 在 Init 时已经绑定

	// 需要更新 handler 的 writer，但是 slog.TextHandler 没有提供直接修改 writer 的方法
	// 最简单的做法是重新创建一个新的 handler 和 logger
	textHandler := slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug, // 保持和 Init 中相同的默认等级
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	logger = slog.New(textHandler) // 创建新的 slog.Logger

	go func() {
		if err := compressLog(backupPath); err != nil { // 压缩备份文件
			LogError("Compression failed: %v", err) // 注意这里也要用 slog 的 Error
		}
		if err := os.Remove(backupPath); err != nil { // 删除备份文件
			LogError("Failed to remove backup file: %v", err) // 注意这里也要用 slog 的 Error
			fmt.Printf("Failed to remove backup file: %v\n", err)
		}
	}()

	return nil // 返回 nil 表示成功
}

// compressLog 压缩日志文件
// 参数：srcPath 源日志文件路径
// 该函数将指定的日志文件压缩为 tar.gz 格式。
func compressLog(srcPath string) error {
	srcFile, err := os.Open(srcPath) // 打开源日志文件
	if err != nil {
		return err // 返回错误
	}
	defer srcFile.Close() // 确保文件在函数结束时关闭

	dstFile, err := os.Create(srcPath + ".tar.gz") // 创建压缩文件
	if err != nil {
		return err // 返回错误
	}
	defer dstFile.Close() // 确保文件在函数结束时关闭

	gzWriter := gzip.NewWriter(dstFile) // 创建 gzip 写入器
	defer gzWriter.Close()              // 确保关闭 gzip 写入器

	tarWriter := tar.NewWriter(gzWriter) // 创建 tar 写入器
	defer tarWriter.Close()              // 确保关闭 tar 写入器

	info, err := srcFile.Stat() // 获取源文件信息
	if err != nil {
		return err // 返回错误
	}

	// 创建 tar 头部信息
	header := &tar.Header{
		Name:    filepath.Base(srcPath),
		Size:    info.Size(),
		Mode:    int64(info.Mode()),
		ModTime: info.ModTime(),
	}

	if err := tarWriter.WriteHeader(header); err != nil { // 写入头部信息
		return err // 返回错误
	}

	if _, err := io.Copy(tarWriter, srcFile); err != nil { // 复制源文件内容到 tar 写入器
		return err // 返回错误
	}

	return nil // 返回 nil 表示成功
}

// 修改快捷日志方法以使用 slog
func LogDumpf(format string, args ...interface{})  { Logf(LevelDump, "[DUMP] "+format, args...) }
func LogDebugf(format string, args ...interface{}) { Logf(LevelDebug, "[DEBUG] "+format, args...) }
func LogInfof(format string, args ...interface{})  { Logf(LevelInfo, "[INFO] "+format, args...) }
func LogWarningf(format string, args ...interface{}) {
	Logf(LevelWarn, "[WARNING] "+format, args...)
}
func LogErrorf(format string, args ...interface{}) { Logf(LevelError, "[ERROR] "+format, args...) }

/*
// 快捷方式调整，指向新的 Logf 方法
var (
	Logw = LogErrorf
	logw = LogWarningf
	logf = LogInfof
)
*/
