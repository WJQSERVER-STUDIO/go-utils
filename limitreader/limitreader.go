package limitreader

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/time/rate"
)

// --- 限速读取器 RateLimitedReader ---

// RateLimitedReader 包装一个 io.Reader，并应用速率限制。
type RateLimitedReader struct {
	r       io.Reader       // 原始读取器 (如: resp.Body)
	limiter *rate.Limiter   // 令牌桶限速器
	ctx     context.Context // 用于取消等待的 Context (通常是请求的 Context)
}

// NewRateLimitedReader 创建一个新的 RateLimitedReader。
// r: 底层的读取器 (如: resp.Body)。
// limit: 速率限制，单位是 Bytes/s (rate.Limit)。
// burst: 令牌桶的突发容量，单位是字节。
// ctx: 与操作关联的 Context (如: 请求 Context)。
func NewRateLimitedReader(r io.Reader, limit rate.Limit, burst int, ctx context.Context) *RateLimitedReader {
	return &RateLimitedReader{
		r:       r,
		limiter: rate.NewLimiter(limit, burst),
		ctx:     ctx,
	}
}

// Read 实现 io.Reader 接口。
func (rlr *RateLimitedReader) Read(p []byte) (n int, err error) {
	// 在读取数据之前，先向限速器申请 len(p) 个字节的许可。
	// WaitN 会阻塞，直到有令牌或 Context 被取消。
	if err := rlr.limiter.WaitN(rlr.ctx, len(p)); err != nil {
		return 0, err // 如果 Context 取消，返回 Context 错误
	}

	// 向底层的 Reader 读取数据
	n, err = rlr.r.Read(p)

	return n, err
}

// Close 实现 io.Closer 接口，转发 Close 调用给底层 Reader。
func (rlr *RateLimitedReader) Close() error {
	if closer, ok := rlr.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// --- 字符串速率解析函数 ---

var (
	// rateRegex 匹配速率字符串，捕获数值和单位
	// 例如: "100", "1.5", "mbps", "KB/s", "b"
	rateRegex = regexp.MustCompile(`^([\d.]+)\s*([a-zA-Z/]*)$`)

	// unitToBytesPerSec 将单位字符串映射到转换为 Bytes/s 的乘数 (小写单位为键)
	unitToBytesPerSec = map[string]float64{
		// 比特单位 (转换为字节，除以 8)
		"bps":  1.0 / 8.0,
		"kbps": 1000.0 / 8.0,
		"mbps": 1000.0 * 1000.0 / 8.0,
		"gbps": 1000.0 * 1000.0 * 1000.0 / 8.0,

		// 字节单位 (1024-based)
		"b":       1.0, // 假设是 B/s (Bytes/s)
		"":        1.0, // 没有单位也假设是 B/s
		"b/s":     1.0,
		"byte":    1.0,
		"bytes/s": 1.0,

		"k":           1024.0, // 缩写 KB/s
		"kb":          1024.0,
		"kb/s":        1024.0,
		"kilobyte":    1024.0,
		"kilobytes/s": 1024.0,

		"m":           1024.0 * 1024.0, // 缩写 MB/s
		"mb":          1024.0 * 1024.0,
		"mb/s":        1024.0 * 1024.0,
		"megabyte":    1024.0 * 1024.0,
		"megabytes/s": 1024.0 * 1024.0,

		"g":           1024.0 * 1024.0 * 1024.0, // 缩写 GB/s
		"gb":          1024.0 * 1024.0 * 1024.0,
		"gb/s":        1024.0 * 1024.0 * 1024.0,
		"gigabyte":    1024.0 * 1024.0 * 1024.0,
		"gigabytes/s": 1024.0 * 1024.0 * 1024.0,
	}
)

// ParseRate 解析人类可读的速度字符串 (例如, "100kbps", "1.5MB/s", "5000")。
// 返回速率，单位是每秒字节数 (rate.Limit)。
func ParseRate(rateStr string) (rate.Limit, error) {
	rateStr = strings.TrimSpace(rateStr)
	if rateStr == "" {
		return 0, fmt.Errorf("rate string cannot be empty")
	}

	match := rateRegex.FindStringSubmatch(rateStr)
	if len(match) < 3 {
		return 0, fmt.Errorf("invalid rate format: %s", rateStr)
	}

	valueStr := match[1]                 // 数值部分
	unitStr := strings.ToLower(match[2]) // 单位部分，转为小写

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in rate '%s': %w", rateStr, err)
	}

	// 查找单位对应的乘数
	multiplier, ok := unitToBytesPerSec[unitStr]
	if !ok {
		return 0, fmt.Errorf("unknown or unsupported rate unit: '%s' in '%s'", match[2], rateStr)
	}

	bytesPerSecond := value * multiplier

	// 确保速率非负
	if bytesPerSecond < 0 {
		return 0, fmt.Errorf("rate cannot be negative: %s", rateStr)
	}

	return rate.Limit(bytesPerSecond), nil
}
