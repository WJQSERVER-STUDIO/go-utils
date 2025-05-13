package limitreader

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync" // 引入 sync 包用于互斥锁

	"golang.org/x/time/rate"
)

// --- 全局限速器 ---

var (
	// globalLimiter 是全局读取速率限速器，初始化为无限速。
	globalLimiter = rate.NewLimiter(rate.Inf, 0)
	// globalLimitMutex 用于保护 globalLimiter 的并发访问。
	globalLimitMutex sync.RWMutex
)

// SetGlobalRateLimit 设置全局读取速率限制。
// limit: 全局速率限制，单位是 Bytes/s (rate.Limit)。rate.Inf 表示无限制。
// burst: 全局令牌桶的突发容量，单位是字节。
// 将 limit 设置为 <= 0 或 rate.Inf 将禁用全局限速。
func SetGlobalRateLimit(limit rate.Limit, burst int) {
	globalLimitMutex.Lock() // 加写锁以修改全局限速器
	defer globalLimitMutex.Unlock()

	// 如果 limit 非正或为 Inf，则创建无限速的全局限速器
	if limit <= 0 || limit == rate.Inf {
		globalLimiter = rate.NewLimiter(rate.Inf, 0)
	} else {
		globalLimiter = rate.NewLimiter(limit, burst)
	}
}

// --- 限速读取器 RateLimitedReader ---

// RateLimitedReader 包装一个 io.Reader，并应用速率限制。
// 它同时受自身独立限速器和全局限速器的约束。
type RateLimitedReader struct {
	r       io.Reader       // 原始读取器 (如: resp.Body)
	limiter *rate.Limiter   // 独立令牌桶限速器
	ctx     context.Context // 用于取消等待的 Context (通常是请求的 Context)
}

// NewRateLimitedReader 创建一个新的 RateLimitedReader。
// r: 底层的读取器 (如: resp.Body)。
// limit: 独立速率限制，单位是 Bytes/s (rate.Limit)。rate.Inf 表示无限制。
// burst: 独立令牌桶的突发容量，单位是字节。
// ctx: 与操作关联的 Context (如: 请求 Context)。
// 将 limit 设置为 <= 0 或 rate.Inf 将禁用此读取器的独立限速。
func NewRateLimitedReader(r io.Reader, limit rate.Limit, burst int, ctx context.Context) *RateLimitedReader {
	// 如果 limit 非正或为 Inf，则创建无限速的独立限速器
	individualLimiter := rate.NewLimiter(rate.Inf, 0)
	if limit > 0 && limit != rate.Inf {
		individualLimiter = rate.NewLimiter(limit, burst)
	}

	return &RateLimitedReader{
		r:       r,
		limiter: individualLimiter,
		ctx:     ctx,
	}
}

// Read 实现 io.Reader 接口。
// 在读取数据之前，先向全局限速器申请许可，再向独立限速器申请许可（如果它们有限速的话）。
func (rlr *RateLimitedReader) Read(p []byte) (n int, err error) {
	bytesToRequest := len(p)
	if bytesToRequest == 0 {
		// 请求读取 0 字节时，直接调用底层 Read 并立即返回
		return rlr.r.Read(p)
	}

	// 获取当前的全局限速器 (使用读锁保证并发安全)
	globalLimitMutex.RLock()
	currentGlobalLimiter := globalLimiter // 获取一个当前全局限速器的引用
	globalLimitMutex.RUnlock()

	// 检查全局限速器和独立限速器是否都处于激活状态 (即非无限速)
	globalLimitActive := currentGlobalLimiter.Limit() != rate.Inf
	individualLimitActive := rlr.limiter.Limit() != rate.Inf

	// 如果全局和独立限速器都未激活，则跳过 WaitN 调用，直接执行底层读取以提升性能
	if !globalLimitActive && !individualLimitActive {
		return rlr.r.Read(p)
	}

	// 如果全局限速器激活，先等待全局许可
	if globalLimitActive {
		// WaitN 会阻塞直到有令牌或 Context 被取消
		if err := currentGlobalLimiter.WaitN(rlr.ctx, bytesToRequest); err != nil {
			// 如果 Context 取消，WaitN 会返回 Context 错误
			return 0, err
		}
	}

	// 如果独立限速器激活，再等待独立许可
	if individualLimitActive {
		// WaitN 内部会检查 Context，即使在全局等待时 Context 已取消，这里也会正确处理
		if err := rlr.limiter.WaitN(rlr.ctx, bytesToRequest); err != nil {
			// 如果 Context 取消，WaitN 会返回 Context 错误
			return 0, err
		}
	}

	// 向底层的 Reader 读取数据
	n, err = rlr.r.Read(p)

	// 注意：如前所述，WaitN 是在读取之前申请 len(p) 个字节的令牌。
	// 如果底层 Read 实际读取的字节数 n 小于 len(p)，我们为未读取的字节也消耗了令牌。
	// 这是使用 WaitN 的一种权衡，它确保了严格的预读速率控制，实现简单。
	// 更复杂的实现可以在读取后根据实际读取的 n 调用 TryTakeN 或 AllowN 退回未使用的令牌，
	// 但会增加复杂性。对于大多数场景，当前方法已足够。

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
// 如果解析结果为非正数，则返回错误。对于表示无限制的情况，应在调用 ParseRate 后检查结果
// 或使用 rate.Inf 直接传递给 NewRateLimitedReader 或 SetGlobalRateLimit。
func ParseRate(rateStr string) (rate.Limit, error) {
	rateStr = strings.TrimSpace(rateStr)
	if rateStr == "" {
		// 空字符串被视为无效输入，而不是无限速
		return 0, fmt.Errorf("rate string cannot be empty")
	}

	match := rateRegex.FindStringSubmatch(rateStr)
	if len(match) < 3 {
		// rateRegex 期望至少匹配一个数值部分
		return 0, fmt.Errorf("invalid rate format: %s", rateStr)
	}

	valueStr := match[1]                 // 数值部分 (例如 "100" 或 "1.5")
	unitStr := strings.ToLower(match[2]) // 单位部分，转为小写 (例如 "kbps" 或 "mb/s")

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

	// 确保计算出的速率是正数。0 或负数速率被视为无效，无限速应使用 rate.Inf。
	if bytesPerSecond <= 0 {
		return 0, fmt.Errorf("calculated rate is non-positive (%.2f B/s) from '%s'", bytesPerSecond, rateStr)
	}

	return rate.Limit(bytesPerSecond), nil
}
