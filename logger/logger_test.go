package logger

import (
	"testing"
)

func BenchmarkLogInfo(b *testing.B) {
	// 初始化日志记录器
	err := Init("test.log", 10) // 设置日志文件路径和最大大小
	if err != nil {
		b.Fatalf("Failed to initialize logger: %v", err)
	}
	defer Close() // 确保在测试结束时关闭日志

	// 设置迭代次数为 200000
	for i := 0; i < 200000; i++ {
		LogInfo("This is an info log message %d", i)
	}
}
